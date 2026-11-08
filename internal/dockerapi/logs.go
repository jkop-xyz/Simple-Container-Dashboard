package dockerapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// maxLogLine bounds one log line; longer lines are split.
const maxLogLine = 1 << 20

// LogOptions choose what Logs streams.
type LogOptions struct {
	// Tail is how many of the most recent lines to start with.
	Tail int
	// Follow keeps streaming new output until the container stops.
	Follow bool
	// Timestamps prefixes each line with the runtime's RFC 3339 timestamp.
	Timestamps bool
	// Tty says the container has a terminal, so its output isn't multiplexed.
	Tty bool
}

// Logs streams a container's output (stdout and stderr interleaved) line by line to fn.
// It stops when the output ends, the container stops (with Follow) or ctx is cancelled.
func (c *Client) Logs(ctx context.Context, id string, opts LogOptions, fn func(line string) error) error {
	q := url.Values{"stdout": {"true"}, "stderr": {"true"}, "tail": {fmt.Sprint(opts.Tail)}}
	if opts.Follow {
		q.Set("follow", "true")
	}
	if opts.Timestamps {
		q.Set("timestamps", "true")
	}
	tty := opts.Tty
	resp, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/logs?"+q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r io.Reader = resp.Body
	if !tty {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(demux(resp.Body, pw)) }()
		defer pr.Close()
		r = pr
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLogLine)
	for sc.Scan() {
		if err := fn(strings.TrimRight(sc.Text(), "\r")); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// demux strips the 8-byte frame headers ([stream, 0, 0, 0, size uint32 BE]) the API
// puts around each chunk of a non-TTY container's output.
func demux(r io.Reader, w io.Writer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(w, r, int64(size)); err != nil {
			return err
		}
	}
}
