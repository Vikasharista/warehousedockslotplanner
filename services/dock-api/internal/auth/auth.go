// Package auth signs demo users in with an email and password and keeps them signed in with a session cookie.
//
// The session is an HS256 JWT in an HttpOnly, SameSite=Strict cookie, verified with core-lib's authdirective (the
// same check the GraphQL directives run). API clients may send the same token as "Authorization: Bearer ...".
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/graphql/authdirective"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	// CookieName is the session cookie.
	CookieName = "setuhaul_session"
	issuer     = "setuhaul-dock-api"
	// TooManyAttemptsCode answers a login after too many failures.
	TooManyAttemptsCode = "TOO_MANY_ATTEMPTS"
	minSecretLen        = 32
	minPasswordLen      = 10
	bcryptCost          = 12
)

// Users is what auth needs from the database.
type Users interface {
	UpsertUser(ctx context.Context, email, name, passwordHash string) error
	UserByEmail(ctx context.Context, email string) (*store.User, error)
	TouchLogin(ctx context.Context, userID int64) error
}

// Settings configure sessions and the login limiter.
type Settings struct {
	Secret       string        // HS256 key, at least 32 characters
	SessionTTL   time.Duration // default 12 h
	SecureCookie bool          // true behind HTTPS
	MaxFailures  int           // failed logins per email or client before a pause; default 5
	FailWindow   time.Duration // default 15 min
}

// Service signs users in and verifies sessions.
type Service struct {
	users     Users
	settings  Settings
	directive *authdirective.Directive
	limiter   *limiter
	dummyHash []byte
	now       func() time.Time
}

// New checks the settings and builds the service.
func New(users Users, s Settings) (*Service, error) {
	if len(s.Secret) < minSecretLen {
		return nil, fmt.Errorf("auth: the session secret must be at least %d characters (APP_AUTH_JWT_SECRET)", minSecretLen)
	}
	if s.SessionTTL <= 0 {
		s.SessionTTL = 12 * time.Hour
	}
	if s.MaxFailures <= 0 {
		s.MaxFailures = 5
	}
	if s.FailWindow <= 0 {
		s.FailWindow = 15 * time.Minute
	}
	d, err := authdirective.New(authdirective.Config{Provider: authdirective.ProviderJWT, JWTSecret: s.Secret,
		JWTIssuer: issuer})
	if err != nil {
		return nil, err
	}
	// A hash to compare against for unknown emails, so a missing account costs the same time as a wrong password.
	dummy, err := bcrypt.GenerateFromPassword([]byte("setuhaul-no-such-account"), bcryptCost)
	if err != nil {
		return nil, err
	}
	return &Service{users: users, settings: s, directive: d, limiter: newLimiter(s.MaxFailures, s.FailWindow),
		dummyHash: dummy, now: time.Now}, nil
}

// Account is one demo account from the settings.
type Account struct {
	Email    string
	Password string
	Name     string
}

// ParseAccounts reads "email:password" entries separated by commas. A password may contain colons but not commas;
// the display name is the part of the email before the @.
func ParseAccounts(raw string) ([]Account, error) {
	var out []Account
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		email, password, ok := strings.Cut(entry, ":")
		email = strings.ToLower(strings.TrimSpace(email))
		if !ok || strings.Index(email, "@") < 1 {
			return nil, fmt.Errorf("auth: demo account %q must be email:password", redact(entry))
		}
		if len(password) < minPasswordLen {
			return nil, fmt.Errorf("auth: the password for %s must be at least %d characters", email, minPasswordLen)
		}
		out = append(out, Account{Email: email, Password: password, Name: strings.Split(email, "@")[0]})
	}
	return out, nil
}

func redact(entry string) string {
	email, _, _ := strings.Cut(entry, ":")
	return email + ":***"
}

// EnsureAccounts creates or updates every account (a changed password takes effect on the next start).
func (s *Service) EnsureAccounts(ctx context.Context, accounts []Account) error {
	for _, a := range accounts {
		hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcryptCost)
		if err != nil {
			return err
		}
		if err := s.users.UpsertUser(ctx, a.Email, a.Name, string(hash)); err != nil {
			return err
		}
	}
	return nil
}

// Login checks the password and returns the user and a session token. Wrong email and wrong password give the same
// answer; too many failures for the email or the client give TOO_MANY_ATTEMPTS until the window passes.
func (s *Service) Login(ctx context.Context, email, password, client string) (*store.User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, "", errors.NewBadRequestError("email and password are required")
	}
	now := s.now()
	if s.limiter.blocked(now, "email:"+email) || s.limiter.blocked(now, "client:"+client) {
		return nil, "", errors.New(TooManyAttemptsCode, "too many failed sign-ins; try again in a few minutes")
	}
	user, err := s.users.UserByEmail(ctx, email)
	if err != nil && errors.CodeOf(err) != errors.RecordNotFoundErrorCode {
		return nil, "", err
	}
	hash := s.dummyHash
	if user != nil {
		hash = []byte(user.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || user == nil {
		s.limiter.fail(now, "email:"+email)
		s.limiter.fail(now, "client:"+client)
		return nil, "", errors.NewUnauthenticatedError("wrong email or password")
	}
	s.limiter.reset("email:" + email)
	token, err := s.issue(user, now)
	if err != nil {
		return nil, "", err
	}
	_ = s.users.TouchLogin(ctx, user.UserID)
	return user, token, nil
}

func (s *Service) issue(u *store.User, now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"sub":         strconv.FormatInt(u.UserID, 10),
		"email":       u.Email,
		"name":        u.DisplayName,
		"permissions": []string{"*"},
		"iss":         issuer,
		"iat":         now.Unix(),
		"exp":         now.Add(s.settings.SessionTTL).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.settings.Secret))
}

// Authenticate verifies the request's session (cookie, or a bearer token) and returns a context carrying the user.
func (s *Service) Authenticate(r *http.Request) (context.Context, error) {
	header := r.Header.Get(constant.HeaderAuthorization)
	if header == "" {
		if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			header = "Bearer " + c.Value
		}
	}
	if header == "" {
		return nil, errors.NewUnauthenticatedError("sign in first")
	}
	return s.directive.Verify(context.WithValue(r.Context(), constant.CtxAuthorization, header))
}

// Claims reads the signed-in user's email and name from a verified request.
func (s *Service) Claims(r *http.Request) (email, name string) {
	raw := r.Header.Get(constant.HeaderAuthorization)
	raw = strings.TrimPrefix(raw, "Bearer ")
	if raw == "" {
		if c, err := r.Cookie(CookieName); err == nil {
			raw = c.Value
		}
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"})).ParseWithClaims(raw, claims,
		func(*jwt.Token) (interface{}, error) { return []byte(s.settings.Secret), nil }); err != nil {
		return "", ""
	}
	email, _ = claims["email"].(string)
	name, _ = claims["name"].(string)
	return email, name
}

// SessionCookie sets (token != "") or clears the session cookie.
func (s *Service) SessionCookie(token string) *http.Cookie {
	c := &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.settings.SecureCookie,
		SameSite: http.SameSiteStrictMode}
	if token == "" {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(s.settings.SessionTTL.Seconds())
	}
	return c
}

// limiter counts failures per key inside a sliding window.
type limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, failures: map[string][]time.Time{}}
}

func (l *limiter) recent(now time.Time, key string) []time.Time {
	kept := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}

func (l *limiter) blocked(now time.Time, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(now, key)) >= l.max
}

func (l *limiter) fail(now time.Time, key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.recent(now, key), now)
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}
