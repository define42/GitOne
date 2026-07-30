package httpio

import (
	"bytes"
	"errors"
	"io"
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

func TestBodyTooLargeRejectsOtherErrors(t *testing.T) {
	if BodyTooLarge(errors.New("other")) {
		t.Fatal("ordinary error was classified as an oversized body")
	}
}
