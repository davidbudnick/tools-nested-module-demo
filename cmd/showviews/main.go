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

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	bin := os.Getenv("GOPLS")
	if bin == "" {
		bin = "gopls"
	}
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
	src, err := os.ReadFile(filepath.Join(root, "other", "main.go"))
	if err != nil {
		fatal(err)
	}
	c.call("initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"configuration":    true,
				"workspaceFolders": true,
			},
		},
		"workspaceFolders": []map[string]string{{"uri": rootURI, "name": "demo"}},
	})
	c.notify("initialized", struct{}{})
	c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        fileURI(filepath.Join(root, "other", "main.go")),
			"languageId": "go",
			"version":    1,
			"text":       string(src),
		},
	})

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		raw := c.call("workspace/executeCommand", map[string]any{"command": "gopls.views"})
		var wrap struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &wrap) != nil || len(wrap.Error) > 0 || len(wrap.Result) == 0 || string(wrap.Result) == "null" {
			continue
		}
		var views []map[string]any
		if json.Unmarshal(wrap.Result, &views) != nil || len(views) == 0 {
			continue
		}
		out, err := json.MarshalIndent(relViews(root, views), "", "  ")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(out))
		fmt.Fprintf(os.Stderr, "views=%d gopls=%s\n", len(views), bin)
		c.call("shutdown", nil)
		c.notify("exit", nil)
		return
	}
	fatal(fmt.Errorf("timed out waiting for gopls.views from %s", bin))
}

type client struct {
	w    io.Writer
	r    *bufio.Reader
	mu   sync.Mutex
	id   int
	ch   chan []byte
	done chan error
}

// newClient wraps gopls stdio JSON-RPC.
func newClient(w io.Writer, r io.Reader) *client {
	return &client{w: w, r: bufio.NewReader(r), ch: make(chan []byte, 64), done: make(chan error, 1)}
}

// readLoop demuxes server requests vs responses.
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

// call sends a JSON-RPC request and waits for its response.
func (c *client) call(method string, params any) []byte {
	c.id++
	id := c.id
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	timeout := time.After(15 * time.Second)
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

// notify sends a JSON-RPC notification.
func (c *client) notify(method string, params any) {
	c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// handle answers gopls server requests.
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
		result = []any{map[string]any{"directoryFilters": []string{"-other"}}}
	case "client/registerCapability", "window/workDoneProgress/create":
		result = nil
	default:
		return
	}
	c.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
}

// write frames and sends one LSP message.
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

// readLSP reads one Content-Length framed LSP message.
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

// relViews rewrites Root and Folder to paths relative to root.
func relViews(root string, views []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		out = append(out, map[string]any{
			"Type":   v["Type"],
			"Root":   relURI(root, fmt.Sprint(v["Root"])),
			"Folder": relURI(root, fmt.Sprint(v["Folder"])),
		})
	}
	return out
}

// relURI maps a file:// URI to a slash-separated path relative to root.
func relURI(root, uri string) string {
	p := strings.TrimPrefix(uri, "file://")
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return uri
	}
	if rel == "." || rel == "" {
		return "."
	}
	return filepath.ToSlash(rel)
}

// fileURI returns a file:// URI for path.
func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		fatal(err)
	}
	return "file://" + filepath.ToSlash(abs)
}

// fatal prints err and exits.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
