package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// Resolved is the fully-resolved set of parameters the agent needs at
// startup that cannot be derived from local config alone.
type Resolved struct {
	ServerInfo labbridge.ServerInfo
	RemotePort int
}

// Options carries everything Resolve needs. The retry parameters have
// sensible defaults; tests pass tiny values to keep them fast.
type Options struct {
	HTTPClient *http.Client
	Base       string // "https://<host>" (or http://, for tests)
	User       string
	Pass       string
	CachePath  string
	UserAgent  string

	// Timeouts. Zero values trigger production defaults:
	//   FetchTimeout   = 5s   (per parallel fetch attempt)
	//   InitialBackoff = 1s
	//   MaxBackoff     = 1m
	FetchTimeout   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func (o Options) fetchTimeout() time.Duration {
	if o.FetchTimeout > 0 {
		return o.FetchTimeout
	}
	return 5 * time.Second
}

func (o Options) initialBackoff() time.Duration {
	if o.InitialBackoff > 0 {
		return o.InitialBackoff
	}
	return 1 * time.Second
}

func (o Options) maxBackoff() time.Duration {
	if o.MaxBackoff > 0 {
		return o.MaxBackoff
	}
	return 1 * time.Minute
}

// Resolve performs one or more parallel attempts to fetch /server-info
// and /clients/{user}, falling back to a user-anchored disk cache and
// then to an exponential-backoff retry loop. See the design doc for
// the full algorithm.
func Resolve(ctx context.Context, opts Options) (Resolved, error) {
	slog.Info("bootstrap resolve start", "host", opts.Base, "user", opts.User)
	backoff := opts.initialBackoff()
	maxBackoff := opts.maxBackoff()
	for {
		res, sawUnauthorized, err := tryLive(ctx, opts)
		if err == nil {
			if writeErr := WriteCache(opts.CachePath, cacheFromResolved(res, opts.User)); writeErr != nil {
				slog.Warn("bootstrap: write cache failed", "err", writeErr)
			}
			slog.Info("bootstrap resolve ok", "host", opts.Base, "source", "remote")
			return res, nil
		}
		if ctx.Err() != nil {
			slog.Error("bootstrap resolve failed", "host", opts.Base, "err", ctx.Err().Error())
			return Resolved{}, ctx.Err()
		}
		slog.Warn("bootstrap: live fetch failed", "err", err, "unauthorized", sawUnauthorized)

		if !sawUnauthorized {
			if c, cacheErr := ReadCache(opts.CachePath, opts.User); cacheErr == nil {
				slog.Warn("bootstrap: serving cached server-info while live is unavailable",
					"cache_user", c.User, "fetched_at", c.FetchedAt)
				slog.Info("bootstrap resolve ok", "host", opts.Base, "source", "cache")
				return Resolved{ServerInfo: c.ServerInfo, RemotePort: c.RemotePort}, nil
			}
		}

		select {
		case <-ctx.Done():
			slog.Error("bootstrap resolve failed", "host", opts.Base, "err", ctx.Err().Error())
			return Resolved{}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// tryLive does one parallel pair of fetches. Returns (resolved, sawUnauthorized, error).
// sawUnauthorized is true iff FetchClient returned ErrUnauthorized;
// callers use it to decide whether to consult the cache.
func tryLive(ctx context.Context, opts Options) (Resolved, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, opts.fetchTimeout())
	defer cancel()

	var (
		wg        sync.WaitGroup
		info      labbridge.ServerInfo
		infoErr   error
		client    labbridge.ClientInfo
		clientErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		info, infoErr = labbridge.FetchServerInfo(cctx, opts.HTTPClient, opts.Base, opts.UserAgent)
	}()
	go func() {
		defer wg.Done()
		client, clientErr = labbridge.FetchClient(cctx, opts.HTTPClient, opts.Base, opts.User, opts.Pass, opts.UserAgent)
	}()
	wg.Wait()

	sawUnauthorized := errors.Is(clientErr, labbridge.ErrUnauthorized)

	if infoErr != nil {
		return Resolved{}, sawUnauthorized, fmt.Errorf("bootstrap: server-info: %w", infoErr)
	}
	if clientErr != nil {
		return Resolved{}, sawUnauthorized, fmt.Errorf("bootstrap: clients: %w", clientErr)
	}
	return Resolved{ServerInfo: info, RemotePort: client.Port}, false, nil
}

func cacheFromResolved(r Resolved, user string) Cache {
	return Cache{
		Version:    cacheCurrentVersion,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		User:       user,
		ServerInfo: r.ServerInfo,
		RemotePort: r.RemotePort,
	}
}
