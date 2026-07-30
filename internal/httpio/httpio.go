package httpio

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// DefaultIdleTimeout bounds one blocked network read or write while allowing
// transfers of any duration as long as they continue making progress.
const DefaultIdleTimeout = 30 * time.Second

type deadlineReadCloser struct {
	body       io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
}

func (r *deadlineReadCloser) Read(buffer []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(r.timeout))
	return r.body.Read(buffer)
}

func (r *deadlineReadCloser) Close() error {
	return r.body.Close()
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	wrote      bool
}

func (w *deadlineResponseWriter) Write(buffer []byte) (int, error) {
	w.wrote = true
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	return w.ResponseWriter.Write(buffer)
}

func (w *deadlineResponseWriter) WriteHeader(status int) {
	w.wrote = true
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	w.ResponseWriter.WriteHeader(status)
}

func (w *deadlineResponseWriter) Flush() {
	w.wrote = true
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	_ = w.controller.Flush()
}

func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type deadlineWriter struct {
	output     io.Writer
	controller *http.ResponseController
	timeout    time.Duration
	wrote      bool
}

func (w *deadlineWriter) Write(buffer []byte) (int, error) {
	w.wrote = true
	if w.controller != nil {
		_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	}
	return w.output.Write(buffer)
}

// Protect applies a hard request-body limit and sliding read/write deadlines.
// The returned cleanup clears connection deadlines for HTTP keep-alive reuse.
func Protect(
	response http.ResponseWriter,
	request *http.Request,
	timeout time.Duration,
	maximumBodyBytes int64,
) (http.ResponseWriter, func()) {
	controller := http.NewResponseController(response)
	if request.Body != nil {
		_ = controller.SetReadDeadline(time.Now().Add(timeout))
		body := request.Body
		if maximumBodyBytes >= 0 {
			body = http.MaxBytesReader(response, body, maximumBodyBytes)
		}
		request.Body = &deadlineReadCloser{
			body:       body,
			controller: controller,
			timeout:    timeout,
		}
	}
	writer := &deadlineResponseWriter{
		ResponseWriter: response,
		controller:     controller,
		timeout:        timeout,
	}
	return writer, func() {
		if writer.wrote {
			_ = controller.SetWriteDeadline(time.Now().Add(timeout))
			_ = controller.Flush()
		}
		if request.Body != nil {
			_ = request.Body.Close()
		}
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
	}
}

// ProtectWriter adds a sliding write deadline to a streamed response body.
func ProtectWriter(output io.Writer, timeout time.Duration) (io.Writer, func()) {
	var controller *http.ResponseController
	if response, ok := output.(http.ResponseWriter); ok {
		controller = http.NewResponseController(response)
	}
	writer := &deadlineWriter{
		output:     output,
		controller: controller,
		timeout:    timeout,
	}
	return writer, func() {
		if controller != nil {
			if writer.wrote {
				_ = controller.SetWriteDeadline(time.Now().Add(timeout))
				_ = controller.Flush()
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

// BodyTooLarge reports whether err was caused by a Protect body limit.
func BodyTooLarge(err error) bool {
	var maximum *http.MaxBytesError
	return errors.As(err, &maximum)
}
