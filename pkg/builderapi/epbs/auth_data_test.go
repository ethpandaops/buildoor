package epbs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultAuthData covers the vectors from the builder-specs default auth
// data section.
func TestDefaultAuthData(t *testing.T) {
	vectors := []struct {
		url      string
		expected string
	}{
		{"https://builder.example.com/", "builder.example.com"},
		{"HTTPS://Builder.Example.com:443/bids?x=1", "builder.example.com"},
		{"https://builder.example.com:8080", "builder.example.com"},
		{"https://user:pw@builder.example.com/", "builder.example.com"},
		{"https://10.0.0.5:18550/eth/v1/builder", "10.0.0.5"},
		{"https://[0:0:0:0:0:0:0:1]:8443/", "[::1]"},
		{"https://[::ffff:192.0.2.1]/", "[::ffff:c000:201]"},
		{"http://buildoor:8080", "buildoor"},
	}

	for _, v := range vectors {
		data, err := defaultAuthData(v.url)
		require.NoError(t, err, v.url)
		assert.Equal(t, v.expected, string(data), v.url)
	}
}

func TestDefaultAuthData_Invalid(t *testing.T) {
	for _, builderURL := range []string{"", "builder.example.com", "https://", "https://bücher.example"} {
		_, err := defaultAuthData(builderURL)
		assert.Error(t, err, builderURL)
	}
}

func TestMatchesAuthData(t *testing.T) {
	const builderURL = "https://Builder.Example.com:8443/"

	// The builder-specs default derived from the URL
	assert.True(t, matchesAuthData([]byte("builder.example.com"), builderURL))
	// The URL bytes verbatim, as signed by validator clients predating the default
	assert.True(t, matchesAuthData([]byte(builderURL), builderURL))

	assert.False(t, matchesAuthData([]byte("https://builder.example.com"), builderURL))
	assert.False(t, matchesAuthData([]byte("other-builder.example.com"), builderURL))
	assert.False(t, matchesAuthData(nil, builderURL))

	// A URL without a hostname can only match verbatim
	assert.True(t, matchesAuthData([]byte("buildoor:8080"), "buildoor:8080"))
	assert.False(t, matchesAuthData([]byte("buildoor"), "buildoor:8080"))
}
