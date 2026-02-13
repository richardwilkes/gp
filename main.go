package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/richardwilkes/toolbox/v2/errs"
	"github.com/richardwilkes/toolbox/v2/xflag"
	"github.com/richardwilkes/toolbox/v2/xio"
	"github.com/richardwilkes/toolbox/v2/xos"
	"github.com/richardwilkes/toolbox/v2/xstrings"
	"github.com/richardwilkes/toolbox/v2/xterm"
	"github.com/yookoala/realpath"
)

type repo struct {
	printer chan *msgInfo
	path    string
	row     int
	col     int
}

type msgInfo struct {
	msg   string
	color string
	row   int
	col   int
}

func main() {
	xos.AppVersion = "1.2.1"
	xos.CopyrightStartYear = "2022"
	xos.CopyrightHolder = "Richard A. Wilkes"
	xos.AppIdentifier = "com.trollworks.gp"
	xflag.SetUsage(nil, "Pulls unmodified git repos", "[dir]...")
	xflag.AddVersionFlags()
	xflag.Parse()
	paths := flag.Args()

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
	sort.Slice(list, func(i, j int) bool { return xstrings.NaturalLess(list[i], list[j], true) })

	var printerWG sync.WaitGroup
	printer := make(chan *msgInfo, len(list))
	t := xterm.NewAnsiWriter(os.Stdout)
	t.Clear()
	printerWG.Go(func() { processMsgs(t, printer) })

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
			color: t.Kind().Reset(),
			row:   i + 1,
			col:   1,
		}
		wg.Go(func() { processRepo(t.Kind(), repos[i]) })
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

func processMsgs(t *xterm.AnsiWriter, printer chan *msgInfo) {
	maxRow := 1
	for m := range printer {
		if maxRow < m.row {
			maxRow = m.row
		}
		t.WriteString(m.color)
		t.Position(m.row, m.col)
		msg := m.msg
		if i := strings.Index(msg, "\n"); i != -1 {
			msg = msg[:i]
		}
		t.WriteString(msg)
		t.Reset()
		t.EraseLineToEnd()
	}
	t.Reset()
	t.Position(maxRow+1, 1)
}

func processRepo(k xterm.Kind, r *repo) {
	branch, err := r.git(k, "branch", "--show-current")
	if err != nil {
		r.printer <- &msgInfo{
			msg:   "skipped due to error: " + err.Error(),
			color: k.Bold() + k.Red(),
			row:   r.row,
			col:   r.col,
		}
		return
	}
	r.printer <- &msgInfo{
		msg:   "[",
		color: k.Reset(),
		row:   r.row,
		col:   r.col,
	}
	r.col++
	r.printer <- &msgInfo{
		msg:   branch,
		color: k.Bold(),
		row:   r.row,
		col:   r.col,
	}
	r.col += len(branch)
	r.printer <- &msgInfo{
		msg:   "]",
		color: k.Reset(),
		row:   r.row,
		col:   r.col,
	}
	r.col += 2
	var out string
	if out, err = r.git(k, "status", "--porcelain"); err != nil {
		r.printer <- &msgInfo{
			msg:   "skipped due to error: " + err.Error(),
			color: k.Bold() + k.Red(),
			row:   r.row,
			col:   r.col,
		}
		return
	}
	if out != "" {
		r.printer <- &msgInfo{
			msg:   "skipped due to changes",
			color: k.Bold() + k.Magenta(),
			row:   r.row,
			col:   r.col,
		}
		return
	}
	if out, err = r.git(k, "pull"); err != nil {
		r.printer <- &msgInfo{
			msg:   "failed to pull: " + err.Error(),
			color: k.Bold() + k.Red(),
			row:   r.row,
			col:   r.col,
		}
		return
	}
	for s := range strings.SplitSeq(out, "\n") {
		if strings.Contains(s, " changed, ") {
			r.printer <- &msgInfo{
				msg:   strings.TrimSpace(s),
				color: k.Bold() + k.Magenta(),
				row:   r.row,
				col:   r.col,
			}
			return
		}
	}
	r.printer <- &msgInfo{
		msg:   "no changes",
		color: k.Blue(),
		row:   r.row,
		col:   r.col,
	}
}

func (r *repo) git(k xterm.Kind, args ...string) (result string, err error) {
	for i := range 5 {
		if i != 0 {
			time.Sleep(time.Second)
		}
		result, err = r.gitActual(args...)
		if err == nil {
			return result, nil
		}
		r.printer <- &msgInfo{
			msg:   fmt.Sprintf("retry #%d for %s", i+1, err.Error()),
			color: k.Bold() + k.Magenta(),
			row:   r.row,
			col:   r.col,
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
