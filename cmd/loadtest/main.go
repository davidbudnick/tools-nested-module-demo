package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var nMods = 16
var nFiles = 40

func main() {
	if v := os.Getenv("LOAD_N"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fatal(err)
		}
		nMods = n
	}
	if v := os.Getenv("LOAD_FILES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fatal(err)
		}
		nFiles = n
	}
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	if err := writeLoad(root); err != nil {
		fatal(err)
	}
	stock := os.Getenv("GOPLS_STOCK")
	patched := os.Getenv("GOPLS_PATCHED")
	if stock == "" || patched == "" {
		fatal(fmt.Errorf("set GOPLS_STOCK and GOPLS_PATCHED"))
	}
	fmt.Printf("load: %d nested modules × %d files\n", nMods, nFiles)
	sViews, sRSS := run(root, stock)
	pViews, pRSS := run(root, patched)
	fmt.Printf("\n%-8s %6s %8s\n", "", "views", "rss_mb")
	fmt.Printf("%-8s %6d %8.0f\n", "stock", sViews, sRSS)
	fmt.Printf("%-8s %6d %8.0f\n", "patched", pViews, pRSS)
	if sViews <= pViews {
		fatal(fmt.Errorf("expected stock views > patched, got %d vs %d", sViews, pViews))
	}
	if sRSS <= pRSS*1.3 && sViews > pViews+2 {
		fmt.Fprintf(os.Stderr, "warn: RSS delta small (%.0f vs %.0f) with view gap %d\n", sRSS, pRSS, sViews-pViews)
	}
}

// writeLoad creates nested modules under load/mXX/.
func writeLoad(root string) error {
	base := filepath.Join(root, "load")
	if err := os.RemoveAll(base); err != nil {
		return err
	}
	for m := 1; m <= nMods; m++ {
		dir := filepath.Join(base, fmt.Sprintf("m%02d", m))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		mod := fmt.Sprintf("module example.com/load/m%02d\n\ngo 1.22\n", m)
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
			return err
		}
		for f := 0; f < nFiles; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("p%02d.go", f)), []byte(genFile(m, f)), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// genFile returns unique typecheck fodder for one package file.
func genFile(mod, file int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package p%02d\n\n", file)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "type T%02d%02d%02d struct { A, B, C, D, E string; N, M, P, Q, R int }\n", mod, file, i)
		fmt.Fprintf(&b, "func (t T%02d%02d%02d) Sum() int { return t.N + t.M + t.P + t.Q + t.R }\n", mod, file, i)
		fmt.Fprintf(&b, "func NewT%02d%02d%02d() T%02d%02d%02d { return T%02d%02d%02d{A: \"foo\", B: \"bar\", N: %d} }\n",
			mod, file, i, mod, file, i, mod, file, i, mod*1000+file*20+i)
	}
	return b.String()
}

// run starts gopls, didOpens one file per nested module, returns view count and RSS MiB.
func run(root, bin string) (int, float64) {
	cmd := exec.Command(bin, "serve")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	defer cmd.Process.Kill()
	c := newClient(stdin, stdout)
	go c.readLoop()
	rootURI := fileURI(root)
	c.call("initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true},
		},
		"workspaceFolders": []map[string]string{{"uri": rootURI, "name": "demo"}},
	})
	c.notify("initialized", struct{}{})
	for m := 1; m <= nMods; m++ {
		path := filepath.Join(root, "load", fmt.Sprintf("m%02d", m), "p00.go")
		src, err := os.ReadFile(path)
		if err != nil {
			fatal(err)
		}
		c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": fileURI(path), "languageId": "go", "version": 1, "text": string(src),
			},
		})
	}
	n, last, same := 0, 0, 0
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(800 * time.Millisecond)
		raw := c.call("workspace/executeCommand", map[string]any{"command": "gopls.views"})
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &wrap) != nil || len(wrap.Result) == 0 || string(wrap.Result) == "null" {
			continue
		}
		var views []map[string]any
		if json.Unmarshal(wrap.Result, &views) != nil || len(views) == 0 {
			continue
		}
		n = len(views)
		if n == last {
			same++
			if same >= 5 {
				break
			}
		} else {
			last, same = n, 0
		}
	}
	if n == 0 {
		fatal(fmt.Errorf("%s: no views", bin))
	}
	time.Sleep(15 * time.Second)
	rss := rssMB(cmd.Process.Pid)
	c.call("shutdown", nil)
	c.notify("exit", nil)
	return n, rss
}

func rssMB(pid int) float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return float64(kb) / 1024
}

type client struct {
	w    io.Writer
	r    *bufio.Reader
	mu   sync.Mutex
	id   int
	ch   chan []byte
	done chan error
}

func newClient(w io.Writer, r io.Reader) *client {
	return &client{w: w, r: bufio.NewReader(r), ch: make(chan []byte, 128), done: make(chan error, 1)}
}

func (c *client) readLoop() {
	for {
		msg, err := readLSP(c.r)
		if err != nil {
			c.done <- err
			return
		}
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(msg, &env) == nil && env.Method != "" && len(env.ID) > 0 {
			c.handle(msg)
			continue
		}
		select {
		case c.ch <- msg:
		default:
		}
	}
}

func (c *client) call(method string, params any) []byte {
	c.id++
	id := c.id
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	timeout := time.After(30 * time.Second)
	for {
		select {
		case err := <-c.done:
			fatal(err)
		case <-timeout:
			fatal(fmt.Errorf("no response for %s", method))
		case msg := <-c.ch:
			var hdr struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg, &hdr) != nil || len(hdr.ID) == 0 {
				continue
			}
			var got int
			if json.Unmarshal(hdr.ID, &got) == nil && got == id {
				return msg
			}
		}
	}
}

func (c *client) notify(method string, params any) {
	c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) handle(msg []byte) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(msg, &req) != nil || req.Method == "" || len(req.ID) == 0 {
		return
	}
	var result any
	switch req.Method {
	case "workspace/configuration":
		result = []any{map[string]any{"directoryFilters": []string{"-other", "-load"}}}
	case "client/registerCapability", "window/workDoneProgress/create":
		result = nil
	default:
		return
	}
	c.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
}

func (c *client) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(b), b); err != nil {
		fatal(err)
	}
}

func readLSP(r *bufio.Reader) ([]byte, error) {
	n := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if n > 0 {
				break
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			v, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, err
			}
			n = v
		}
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		fatal(err)
	}
	return "file://" + filepath.ToSlash(abs)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
