package httpio

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type deadlineRecorder struct {
	header         http.Header
	body           bytes.Buffer
	status         int
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

type pipeResponseWriter struct {
	net.Conn
	header http.Header
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *pipeResponseWriter) WriteHeader(int) {}

func (r *deadlineRecorder) Header() http.Header {
	return r.header
}

func (r *deadlineRecorder) Write(buffer []byte) (int, error) {
	return r.body.Write(buffer)
}

func (r *deadlineRecorder) WriteHeader(status int) {
	r.status = status
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadlines = append(r.readDeadlines, deadline)
	return nil
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.writeDeadlines = append(r.writeDeadlines, deadline)
	return nil
}

func TestProtectLimitsBodiesAndSlidesDeadlines(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("four"))
	response := &deadlineRecorder{header: http.Header{}}
	writer, cleanup := Protect(response, request, time.Minute, 3)

	buffer := make([]byte, 1)
	if _, err := request.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := request.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	_, err := io.ReadAll(request.Body)
	if !BodyTooLarge(err) {
		t.Fatalf("oversized body error = %v", err)
	}
	if _, err = writer.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if len(response.readDeadlines) < 3 ||
		response.readDeadlines[0].IsZero() ||
		response.readDeadlines[1].Before(response.readDeadlines[0]) ||
		response.readDeadlines[2].Before(response.readDeadlines[1]) {
		t.Fatalf("body reads did not slide idle deadlines: %v", response.readDeadlines)
	}
	if len(response.writeDeadlines) == 0 || response.writeDeadlines[0].IsZero() {
		t.Fatal("response write did not install an idle deadline")
	}

	cleanup()
	if response.body.String() != "response" {
		t.Fatalf("response body = %q", response.body.String())
	}
	if !response.readDeadlines[len(response.readDeadlines)-1].IsZero() ||
		!response.writeDeadlines[len(response.writeDeadlines)-1].IsZero() {
		t.Fatal("cleanup did not clear connection deadlines")
	}
}

func TestProtectWriterSlidesAndClearsWriteDeadline(t *testing.T) {
	response := &deadlineRecorder{header: http.Header{}}
	writer, cleanup := ProtectWriter(response, time.Minute)
	if _, err := writer.Write([]byte("archive")); err != nil {
		t.Fatal(err)
	}
	if len(response.writeDeadlines) != 1 || response.writeDeadlines[0].IsZero() {
		t.Fatal("stream write did not install an idle deadline")
	}
	cleanup()
	if len(response.writeDeadlines) < 3 {
		t.Fatal("stream cleanup did not protect the final flush")
	}
	if !response.writeDeadlines[len(response.writeDeadlines)-1].IsZero() {
		t.Fatal("stream cleanup did not clear the write deadline")
	}
}

func TestDeadlineResponseWriterFlushesAndUnwraps(t *testing.T) {
	response := &deadlineRecorder{header: http.Header{}}
	writer := &deadlineResponseWriter{
		ResponseWriter: response,
		controller:     http.NewResponseController(response),
		timeout:        time.Minute,
	}
	writer.Flush()
	if !writer.wrote {
		t.Fatal("flush did not mark the response as written")
	}
	if len(response.writeDeadlines) != 1 || response.writeDeadlines[0].IsZero() {
		t.Fatal("flush did not install a write deadline")
	}
	if writer.Unwrap() != response {
		t.Fatal("response writer did not unwrap to its delegate")
	}
}

func TestProtectInterruptsStalledSocketIO(t *testing.T) {
	const timeout = 25 * time.Millisecond

	t.Run("read", func(t *testing.T) {
		server, client := net.Pipe()
		defer func() {
			_ = server.Close()
			_ = client.Close()
		}()
		response := &pipeResponseWriter{Conn: server, header: http.Header{}}
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Body = server
		_, cleanup := Protect(response, request, timeout, -1)

		result := make(chan error, 1)
		go func() {
			_, err := request.Body.Read(make([]byte, 1))
			result <- err
		}()
		var err error
		select {
		case err = <-result:
		case <-time.After(time.Second):
			t.Fatal("stalled socket read was not interrupted")
		}
		cleanup()
		var networkError net.Error
		if !errors.As(err, &networkError) || !networkError.Timeout() {
			t.Fatalf("stalled socket read error = %v, want timeout", err)
		}
	})

	t.Run("write", func(t *testing.T) {
		server, client := net.Pipe()
		defer func() {
			_ = server.Close()
			_ = client.Close()
		}()
		response := &pipeResponseWriter{Conn: server, header: http.Header{}}
		writer, cleanup := ProtectWriter(response, timeout)

		result := make(chan error, 1)
		go func() {
			_, err := writer.Write([]byte("blocked response"))
			result <- err
		}()
		var err error
		select {
		case err = <-result:
		case <-time.After(time.Second):
			t.Fatal("stalled socket write was not interrupted")
		}
		cleanup()
		var networkError net.Error
		if !errors.As(err, &networkError) || !networkError.Timeout() {
			t.Fatalf("stalled socket write error = %v, want timeout", err)
		}
	})
}

func TestBodyTooLargeRejectsOtherErrors(t *testing.T) {
	if BodyTooLarge(errors.New("other")) {
		t.Fatal("ordinary error was classified as an oversized body")
	}
}
