package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

type Client struct{ http *http.Client }

func NewClient(home string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{}
		return d.DialContext(ctx, "unix", filepath.Join(home, "daemon.sock"))
	}}, Timeout: 70 * time.Second}}
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://atlas"+path, body)
	if e != nil {
		return e
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, e := c.http.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var v struct {
			Error string `json:"error"`
		}
		if e = json.NewDecoder(res.Body).Decode(&v); e != nil {
			return fmt.Errorf("daemon HTTP %d", res.StatusCode)
		}
		return fmt.Errorf("%s", v.Error)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, res.Body)
		return e
	}
	return json.NewDecoder(res.Body).Decode(out)
}
func (c *Client) Ping(ctx context.Context) error {
	var v struct {
		Protocol int `json:"protocol"`
	}
	if e := c.Call(ctx, "GET", "/ping", nil, &v); e != nil {
		return e
	}
	if v.Protocol != 1 {
		return fmt.Errorf("daemon protocol %d is incompatible; stop it before upgrading", v.Protocol)
	}
	return nil
}
