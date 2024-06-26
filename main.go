package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/richardwilkes/toolbox/cmdline"
	"github.com/richardwilkes/toolbox/errs"
	"github.com/richardwilkes/toolbox/txt"
	"github.com/richardwilkes/toolbox/xio"
	"github.com/richardwilkes/toolbox/xio/term"
	"github.com/yookoala/realpath"
)

type repo struct {
	path    string
	printer chan *msgInfo
	row     int
	col     int
}

type msgInfo struct {
	msg   string
	row   int
	col   int
	color term.Color
	style term.Style
}

var (
	blue    = term.Blue
	magenta = term.Magenta
	red     = term.Red
	black   = term.Black
)

func main() {
	cmdline.AppVersion = "1.1"
	cmdline.CopyrightStartYear = "2022"
	cmdline.CopyrightHolder = "Richard A. Wilkes"
	cmdline.AppIdentifier = "com.trollworks.gp"
	cl := cmdline.New(true)
	cl.Description = "Pulls unmodified git repos"
	cl.UsageSuffix = "[zero or more paths to the parent directories of git repos]"
	paths := cl.Parse(os.Args[1:])

	// If no paths specified, use the current directory
	if len(paths) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			return
		}
		paths = append(paths, wd)
	}

	// Collect the git repos to process -- we only look one level deep
	set := make(map[string]struct{})
	for _, path := range paths {
		for _, entry := range readDir(path) {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				p := filepath.Join(path, entry.Name())
				if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && fi.IsDir() {
					if p, err = realpath.Realpath(p); err == nil {
						set[p] = struct{}{}
					}
				}
			}
		}
	}
	list := make([]string, 0, len(set))
	longest := 0
	for p := range set {
		list = append(list, p)
		if len(paths) == 1 {
			p = filepath.Base(p)
		}
		if longest < len(p) {
			longest = len(p)
		}
	}
	sort.Slice(list, func(i, j int) bool { return txt.NaturalLess(list[i], list[j], true) })

	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output(); err == nil && bytes.HasPrefix(out, []byte("Dark")) {
			black = term.White
			blue = term.Cyan
		}
	}

	var printerWG sync.WaitGroup
	printer := make(chan *msgInfo, len(list))
	printerWG.Add(1)
	t := term.NewANSI(os.Stdout)
	t.Clear()
	go processMsgs(&printerWG, t, printer)

	var wg sync.WaitGroup
	repos := make([]*repo, len(list))
	format := fmt.Sprintf("%%%ds:", longest)
	for i, p := range list {
		repos[i] = &repo{
			path:    p,
			printer: printer,
			row:     i + 1,
			col:     longest + 3,
		}
		if len(paths) == 1 {
			p = filepath.Base(p)
		}
		printer <- &msgInfo{
			msg:   fmt.Sprintf(format, p),
			row:   i + 1,
			col:   1,
			color: black,
			style: term.Normal,
		}
		wg.Add(1)
		go processRepo(&wg, repos[i])
	}
	wg.Wait()
	close(printer)
	printerWG.Wait()
}

func readDir(path string) []os.DirEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer xio.CloseIgnoringErrors(f)
	var entries []os.DirEntry
	if entries, err = f.ReadDir(-1); err != nil {
		return nil
	}
	return entries
}

func processMsgs(wg *sync.WaitGroup, t *term.ANSI, printer chan *msgInfo) {
	defer wg.Done()
	maxRow := 1
	for m := range printer {
		if maxRow < m.row {
			maxRow = m.row
		}
		t.Foreground(m.color, m.style)
		t.Position(m.row, m.col)
		msg := m.msg
		if i := strings.Index(msg, "\n"); i != -1 {
			msg = msg[:i]
		}
		fmt.Print(msg)
		t.EraseLineToEnd()
	}
	t.Reset()
	t.Position(maxRow+1, 1)
}

func processRepo(wg *sync.WaitGroup, r *repo) {
	defer wg.Done()
	branch, err := r.git("branch", "--show-current")
	if err != nil {
		r.printer <- &msgInfo{
			msg:   "skipped due to error: " + err.Error(),
			row:   r.row,
			col:   r.col,
			color: red,
			style: term.Bold,
		}
		return
	}
	r.printer <- &msgInfo{
		msg:   "[",
		row:   r.row,
		col:   r.col,
		color: black,
		style: term.Normal,
	}
	r.col++
	r.printer <- &msgInfo{
		msg:   branch,
		row:   r.row,
		col:   r.col,
		color: black,
		style: term.Bold,
	}
	r.col += len(branch)
	r.printer <- &msgInfo{
		msg:   "]",
		row:   r.row,
		col:   r.col,
		color: black,
		style: term.Normal,
	}
	r.col += 2
	var out string
	if out, err = r.git("status", "--porcelain"); err != nil {
		r.printer <- &msgInfo{
			msg:   "skipped due to error: " + err.Error(),
			row:   r.row,
			col:   r.col,
			color: red,
			style: term.Bold,
		}
		return
	}
	if out != "" {
		r.printer <- &msgInfo{
			msg:   "skipped due to changes",
			row:   r.row,
			col:   r.col,
			color: magenta,
			style: term.Bold,
		}
		return
	}
	if out, err = r.git("pull"); err != nil {
		r.printer <- &msgInfo{
			msg:   "failed to pull: " + err.Error(),
			row:   r.row,
			col:   r.col,
			color: red,
			style: term.Bold,
		}
		return
	}
	for _, s := range strings.Split(out, "\n") {
		if strings.Contains(s, " changed, ") {
			r.printer <- &msgInfo{
				msg:   strings.TrimSpace(s),
				row:   r.row,
				col:   r.col,
				color: magenta,
				style: term.Bold,
			}
			return
		}
	}
	r.printer <- &msgInfo{
		msg:   "no changes",
		row:   r.row,
		col:   r.col,
		color: blue,
		style: term.Normal,
	}
}

func (r *repo) git(args ...string) (result string, err error) {
	for i := 0; i < 5; i++ {
		if i != 0 {
			time.Sleep(time.Second)
		}
		result, err = r.gitActual(args...)
		if err == nil {
			return result, nil
		}
		r.printer <- &msgInfo{
			msg:   fmt.Sprintf("retry #%d for %s", i+1, err.Error()),
			row:   r.row,
			col:   r.col,
			color: magenta,
			style: term.Bold,
		}
	}
	return result, err
}

func (r *repo) gitActual(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = r.path
	c.Env = mergeEnvLists([]string{"PWD=" + r.path}, os.Environ())
	rsp, err := c.CombinedOutput()
	if err != nil {
		return "", errs.NewWithCause(c.String(), err)
	}
	return strings.TrimSpace(string(rsp)), nil
}

func mergeEnvLists(in, out []string) []string {
NextVar:
	for _, ikv := range in {
		k := strings.SplitAfterN(ikv, "=", 2)[0] + "="
		for i, okv := range out {
			if strings.HasPrefix(okv, k) {
				out[i] = ikv
				continue NextVar
			}
		}
		out = append(out, ikv)
	}
	return out
}
