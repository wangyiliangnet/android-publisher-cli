package channels

import (
	"context"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// A replacement response is sealed before parsing, so an unexpected schema can
// be investigated locally without minting another token or exposing its value.
type honorTokenRecorder struct {
	Sender
	materials keychainMaterials
	proof     string
}

func (s honorTokenRecorder) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	b, err := s.Sender.Send(ctx, q)
	if err != nil || q.URL != "https://iam.developer.honor.com/auth/token" || q.Method != "POST" || q.Class != transport.Lifecycle {
		return b, err
	}
	secret, err := credentials.NewSecret(b)
	if err != nil {
		clear(b)
		return nil, err
	}
	defer secret.Destroy()
	if err = s.materials.Put(ctx, "honor-token-response-"+s.proof, secret); err != nil {
		clear(b)
		return nil, err
	}
	return b, nil
}

// Only the RFC 6749 token-error enum crosses the diagnostics boundary. Error
// descriptions and arbitrary vendor strings remain private response material.
func oauthErrorCode(o object) string {
	switch code := o.str("error"); code {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope":
		return code
	}
	return ""
}

type honorOAuthError struct {
	code    string
	network *transport.Error
}

func (e *honorOAuthError) Error() string { return "honor OAuth: " + e.code }
func (e *honorOAuthError) Unwrap() error { return fail("honor_oauth_"+e.code, "confirmed_rejected") }

func honorOAuthRejection(status int, b []byte, network *transport.Error) error {
	if status != 400 && status != 401 {
		return nil
	}
	o, err := decode(b)
	if err != nil {
		return nil
	}
	for _, key := range []string{"access_token", "token_type", "expires_in"} {
		if _, exists := o[key]; exists {
			return nil
		}
	}
	if code := oauthErrorCode(o); code != "" {
		return &honorOAuthError{code: code, network: network}
	}
	return nil
}
