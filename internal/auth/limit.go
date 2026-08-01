package auth

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaximumConcurrentAttempts = 4
	defaultFailureThreshold          = 5
	defaultInitialBackoff            = time.Second
	defaultMaximumBackoff            = time.Minute
	defaultAttemptRetention          = 15 * time.Minute
	defaultMaximumAttemptStates      = 100_000
	defaultSecretKDFConcurrency      = 4
	overflowAttemptState             = "overflow"
)

// ErrRateLimited indicates that authentication work was rejected before an
// LDAP bind or token KDF operation could start.
var ErrRateLimited = errors.New("authentication rate limit exceeded")

type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return ErrRateLimited.Error()
}

func (e *rateLimitError) Unwrap() error {
	return ErrRateLimited
}

// RetryAfter returns the delay associated with a rate-limit error.
func RetryAfter(err error) time.Duration {
	var limited *rateLimitError
	if errors.As(err, &limited) && limited.retryAfter > 0 {
		return limited.retryAfter
	}
	return time.Second
}

// AttemptLimiterOptions overrides the authentication limiter's safe defaults.
type AttemptLimiterOptions struct {
	MaximumConcurrent int
	FailureThreshold  int
	InitialBackoff    time.Duration
	MaximumBackoff    time.Duration
}

type attemptState struct {
	failures   int
	inFlight   int
	retryAt    time.Time
	lastAccess time.Time
}

// AttemptLimiter applies the same admission policy independently to a request's
// peer IP and normalized username. Reservations are made before authentication
// starts, which also bounds a burst of simultaneous first attempts.
type AttemptLimiter struct {
	mutex             sync.Mutex
	states            map[string]*attemptState
	maximumConcurrent int
	failureThreshold  int
	initialBackoff    time.Duration
	maximumBackoff    time.Duration
	retention         time.Duration
	maximumStates     int
	nextSweep         time.Time
	now               func() time.Time
}

// NewAttemptLimiter constructs a per-IP and per-username limiter.
func NewAttemptLimiter(options AttemptLimiterOptions) *AttemptLimiter {
	maximumConcurrent := options.MaximumConcurrent
	if maximumConcurrent <= 0 {
		maximumConcurrent = defaultMaximumConcurrentAttempts
	}
	failureThreshold := options.FailureThreshold
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}
	initialBackoff := options.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = defaultInitialBackoff
	}
	maximumBackoff := options.MaximumBackoff
	if maximumBackoff < initialBackoff {
		maximumBackoff = defaultMaximumBackoff
		if maximumBackoff < initialBackoff {
			maximumBackoff = initialBackoff
		}
	}
	return &AttemptLimiter{
		states:            make(map[string]*attemptState),
		maximumConcurrent: maximumConcurrent,
		failureThreshold:  failureThreshold,
		initialBackoff:    initialBackoff,
		maximumBackoff:    maximumBackoff,
		retention:         defaultAttemptRetention,
		maximumStates:     defaultMaximumAttemptStates,
		now:               time.Now,
	}
}

// Begin reserves an authentication attempt. The returned function must be
// called exactly once with the outcome so successful authentication can clear
// backoff and failed authentication can increase it.
func (l *AttemptLimiter) Begin(
	ctx context.Context,
	username string,
) (func(bool), error) {
	if l == nil {
		return func(bool) {}, nil
	}
	keys := []string{"user:" + strings.ToLower(strings.TrimSpace(username))}
	if ip := ClientIP(ctx); ip != "" {
		keys = append(keys, "ip:"+ip)
	}
	now := l.now()

	l.mutex.Lock()
	l.sweepLocked(now)
	keys = l.trackedKeysLocked(keys, now)
	retryAfter := time.Duration(0)
	for _, key := range keys {
		state := l.states[key]
		state.lastAccess = now
		if state.retryAt.After(now) {
			retryAfter = max(retryAfter, state.retryAt.Sub(now))
		}
		if state.inFlight >= l.maximumConcurrent {
			retryAfter = max(retryAfter, l.initialBackoff)
		}
	}
	if retryAfter > 0 {
		l.mutex.Unlock()
		return nil, &rateLimitError{retryAfter: retryAfter}
	}
	for _, key := range keys {
		state := l.states[key]
		state.inFlight++
		state.lastAccess = now
	}
	l.mutex.Unlock()

	var once sync.Once
	return func(success bool) {
		once.Do(func() {
			l.finish(keys, success)
		})
	}, nil
}

func (l *AttemptLimiter) trackedKeysLocked(keys []string, now time.Time) []string {
	tracked := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, exists := l.states[key]; !exists && len(l.states) >= l.maximumStates {
			key = overflowAttemptState
		}
		duplicate := false
		for _, existing := range tracked {
			if existing == key {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if l.states[key] == nil {
			l.states[key] = &attemptState{lastAccess: now}
		}
		tracked = append(tracked, key)
	}
	return tracked
}

func (l *AttemptLimiter) finish(keys []string, success bool) {
	now := l.now()
	l.mutex.Lock()
	defer l.mutex.Unlock()
	for _, key := range keys {
		state := l.states[key]
		if state == nil {
			continue
		}
		if state.inFlight > 0 {
			state.inFlight--
		}
		state.lastAccess = now
		if success {
			state.failures = 0
			state.retryAt = time.Time{}
			continue
		}
		state.failures++
		if state.failures >= l.failureThreshold {
			state.retryAt = now.Add(l.backoff(state.failures))
		}
	}
}

func (l *AttemptLimiter) backoff(failures int) time.Duration {
	delay := l.initialBackoff
	for remaining := failures - l.failureThreshold; remaining > 0; remaining-- {
		if delay >= l.maximumBackoff || delay > l.maximumBackoff/2 {
			return l.maximumBackoff
		}
		delay *= 2
	}
	return min(delay, l.maximumBackoff)
}

func (l *AttemptLimiter) sweepLocked(now time.Time) {
	if !l.nextSweep.IsZero() && now.Before(l.nextSweep) {
		return
	}
	expiredBefore := now.Add(-l.retention)
	for key, state := range l.states {
		if state.inFlight == 0 && state.lastAccess.Before(expiredBefore) {
			delete(l.states, key)
		}
	}
	l.nextSweep = now.Add(time.Minute)
}

type requestAuthContextKey struct{}

type requestAuthContext struct {
	clientIP string
	mutex    sync.Mutex
	retry    time.Duration
}

// WithClientIP records the direct socket peer for per-IP authentication
// limiting. Forwarded headers are deliberately ignored because they are
// attacker-controlled unless a deployment has an explicitly trusted proxy.
func WithClientIP(ctx context.Context, remoteAddress string) context.Context {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = strings.Trim(remoteAddress, "[]")
	}
	if parsed := net.ParseIP(host); parsed != nil {
		host = parsed.String()
	}
	return context.WithValue(
		ctx,
		requestAuthContextKey{},
		&requestAuthContext{clientIP: host},
	)
}

// ClientIP returns the normalized direct peer stored on the request context.
func ClientIP(ctx context.Context) string {
	info, _ := ctx.Value(requestAuthContextKey{}).(*requestAuthContext)
	if info == nil {
		return ""
	}
	return info.clientIP
}

// MarkRequestRateLimited records a resolver rejection for Git and LFS handlers,
// whose authorizer contract otherwise carries only authenticated/allowed bits.
func MarkRequestRateLimited(ctx context.Context, err error) {
	if !errors.Is(err, ErrRateLimited) {
		return
	}
	info, _ := ctx.Value(requestAuthContextKey{}).(*requestAuthContext)
	if info == nil {
		return
	}
	info.mutex.Lock()
	info.retry = max(info.retry, RetryAfter(err))
	info.mutex.Unlock()
}

// RequestRateLimit reports a rate-limit rejection recorded on this request.
func RequestRateLimit(ctx context.Context) (time.Duration, bool) {
	info, _ := ctx.Value(requestAuthContextKey{}).(*requestAuthContext)
	if info == nil {
		return 0, false
	}
	info.mutex.Lock()
	defer info.mutex.Unlock()
	return info.retry, info.retry > 0
}

type secretKDFLimiter struct {
	slots chan struct{}
}

func newSecretKDFLimiter(maximumConcurrent int) *secretKDFLimiter {
	if maximumConcurrent <= 0 {
		maximumConcurrent = 1
	}
	return &secretKDFLimiter{slots: make(chan struct{}, maximumConcurrent)}
}

func (l *secretKDFLimiter) acquire() (func(), error) {
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-l.slots
			})
		}, nil
	default:
		return nil, &rateLimitError{retryAfter: time.Second}
	}
}

// The process-wide pool bounds aggregate token KDF CPU use even when
// independent HTTP server handlers are constructed in the same process.
//
//nolint:gochecknoglobals
var processSecretKDFLimiter = newSecretKDFLimiter(defaultSecretKDFConcurrency)
