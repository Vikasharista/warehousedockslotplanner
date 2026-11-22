package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "a-test-secret-that-is-long-enough-123"

type memUsers struct{ byEmail map[string]*store.User }

func (m *memUsers) UpsertUser(_ context.Context, email, name, hash string) error {
	u := m.byEmail[email]
	if u == nil {
		u = &store.User{UserID: int64(len(m.byEmail) + 1), Email: email}
		m.byEmail[email] = u
	}
	u.DisplayName, u.PasswordHash = name, hash
	return nil
}
func (m *memUsers) UserByEmail(_ context.Context, email string) (*store.User, error) {
	if u := m.byEmail[email]; u != nil {
		return u, nil
	}
	return nil, errors.NewRecordNotFoundError("no account")
}
func (m *memUsers) TouchLogin(context.Context, int64) error { return nil }

func service(t *testing.T) *Service {
	t.Helper()
	s, err := New(&memUsers{byEmail: map[string]*store.User{}}, Settings{Secret: secret, MaxFailures: 3, FailWindow: time.Minute})
	require.NoError(t, err)
	require.NoError(t, s.EnsureAccounts(context.Background(), []Account{{Email: "demo@setuhaul.io", Password: "correct-horse-1", Name: "demo"}}))
	return s
}

func TestSettingsAndAccounts(t *testing.T) {
	_, err := New(&memUsers{}, Settings{Secret: "short"})
	assert.ErrorContains(t, err, "at least 32")

	accounts, err := ParseAccounts(" Demo@SetuHaul.io:correct-horse-1 , reviewer@uni.edu:pass:with:colons ,")
	require.NoError(t, err)
	assert.Equal(t, []Account{
		{Email: "demo@setuhaul.io", Password: "correct-horse-1", Name: "demo"},
		{Email: "reviewer@uni.edu", Password: "pass:with:colons", Name: "reviewer"},
	}, accounts)
	_, err = ParseAccounts("demo@setuhaul.io:short")
	assert.ErrorContains(t, err, "at least 10")
	_, err = ParseAccounts("not-an-email:correct-horse-1")
	assert.ErrorContains(t, err, "email:password")
	assert.NotContains(t, err.Error(), "correct-horse", "errors never print a password")
	none, err := ParseAccounts("")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestLoginIssuesASessionTheDirectiveAccepts(t *testing.T) {
	s := service(t)
	user, token, err := s.Login(context.Background(), " DEMO@setuhaul.io ", "correct-horse-1", "1.2.3.4")
	require.NoError(t, err)
	assert.Equal(t, "demo@setuhaul.io", user.Email)
	assert.NotContains(t, user.PasswordHash, "correct-horse", "only a hash is stored")

	r := httptest.NewRequest("GET", "/api/clock", nil)
	r.AddCookie(s.SessionCookie(token))
	ctx, err := s.Authenticate(r)
	require.NoError(t, err)
	id, _ := utility.GetUserID(ctx)
	assert.Equal(t, "1", id)
	email, name := s.Claims(r)
	assert.Equal(t, "demo@setuhaul.io", email)
	assert.Equal(t, "demo", name)

	// The same token as a bearer header works too.
	b := httptest.NewRequest("GET", "/api/clock", nil)
	b.Header.Set("Authorization", "Bearer "+token)
	_, err = s.Authenticate(b)
	require.NoError(t, err)
}

func TestBadSessionsAreRefused(t *testing.T) {
	s := service(t)
	_, err := s.Authenticate(httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))

	forged, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "1", "iss": issuer,
		"exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("another-secret-another-secret-xx"))
	expired, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "1", "iss": issuer,
		"exp": time.Now().Add(-time.Minute).Unix()}).SignedString([]byte(secret))
	otherIssuer, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "1", "iss": "someone-else",
		"exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(secret))
	for name, tok := range map[string]string{"forged": forged, "expired": expired, "issuer": otherIssuer, "junk": "abc"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(s.SessionCookie(tok))
		_, err := s.Authenticate(r)
		assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err), name)
	}
}

func TestWrongPasswordsAndUnknownEmailsLookTheSameAndAreLimited(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	_, _, errWrong := s.Login(ctx, "demo@setuhaul.io", "wrong-password", "9.9.9.9")
	_, _, errUnknown := s.Login(ctx, "nobody@setuhaul.io", "correct-horse-1", "8.8.8.8")
	assert.Equal(t, errWrong.Error(), errUnknown.Error())
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(errWrong))

	// Three failures for one email: even the right password waits for the window.
	_, _, _ = s.Login(ctx, "demo@setuhaul.io", "wrong-password", "9.9.9.9")
	_, _, _ = s.Login(ctx, "demo@setuhaul.io", "wrong-password", "7.7.7.7")
	_, _, err := s.Login(ctx, "demo@setuhaul.io", "correct-horse-1", "6.6.6.6")
	assert.Equal(t, TooManyAttemptsCode, errors.CodeOf(err))
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	_, _, err = s.Login(ctx, "demo@setuhaul.io", "correct-horse-1", "6.6.6.6")
	assert.NoError(t, err)

	_, _, err = s.Login(ctx, "", "", "1.1.1.1")
	assert.Equal(t, errors.BadRequestErrorCode, errors.CodeOf(err))
}

func TestOneClientTryingManyEmailsIsLimitedToo(t *testing.T) {
	s := service(t)
	for i := 0; i < 3; i++ {
		_, _, _ = s.Login(context.Background(), "user"+strings.Repeat("x", i)+"@a.io", "wrong-password", "5.5.5.5")
	}
	_, _, err := s.Login(context.Background(), "demo@setuhaul.io", "correct-horse-1", "5.5.5.5")
	assert.Equal(t, TooManyAttemptsCode, errors.CodeOf(err))
}

func TestCookieFlags(t *testing.T) {
	s, err := New(&memUsers{}, Settings{Secret: secret, SecureCookie: true})
	require.NoError(t, err)
	c := s.SessionCookie("tok")
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure)
	assert.Equal(t, 12*3600, c.MaxAge)
	assert.Equal(t, -1, s.SessionCookie("").MaxAge)
}
