package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Every API call needs the token: as a bearer token (Foyer, scripts) or as
// the session cookie a browser gets by entering it once. Anything that can
// reach the API can run containers, and other containers on the host can
// reach its port, so there is no way to turn this off.

const cookieName = "hoist_session"

// sessionValue derives the cookie from the token, so changing the token
// signs every browser out and the cookie never holds the token itself.
func sessionValue(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("hoist-session-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) authenticated(r *http.Request) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return equal(strings.TrimSpace(bearer), s.Token)
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return equal(c.Value, s.session)
	}
	return false
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "sign in with the Hoist token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses state-changing requests that a browser sent from
// another origin. Other subdomains of your domain count as other origins, so
// a compromised app next door can't deploy through a signed-in browser.
// Requests without these headers (Foyer, curl) rely on the bearer token.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			site := r.Header.Get("Sec-Fetch-Site")
			if site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "cross-origin request refused")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type sessionInfo struct {
	Authenticated bool `json:"authenticated"`
	ReadOnly      bool `json:"read_only"`
	// Conventional is set when commit messages must be Conventional Commits.
	Conventional bool `json:"conventional"`
}

func (s *Server) sessionInfo(authenticated bool) sessionInfo {
	return sessionInfo{Authenticated: authenticated, ReadOnly: s.ReadOnly, Conventional: s.Config.Git.EnforceConventional()}
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionInfo(s.authenticated(r)))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, 4<<10, &body) {
		return
	}
	if !equal(strings.TrimSpace(body.Token), s.Token) {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		writeError(w, http.StatusUnauthorized, "wrong token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.session, Path: "/",
		MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	writeJSON(w, http.StatusOK, s.sessionInfo(true))
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}
