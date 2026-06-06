//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── TestValidateResponseSchema ───────────────────────────────────────────────

func TestValidateResponseSchema(t *testing.T) {
	const objectSchema = `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`

	t.Run("empty schema passes without validation", func(t *testing.T) {
		assert.NoError(t, validateResponseSchema("", `{"anything":"here"}`))
		assert.NoError(t, validateResponseSchema("   ", `{"anything":"here"}`))
	})

	t.Run("valid JSON matching schema passes", func(t *testing.T) {
		require.NoError(t, validateResponseSchema(objectSchema, `{"name":"Alice"}`),
			"response matching schema must pass validation")
	})

	t.Run("valid JSON not matching schema returns schema_mismatch", func(t *testing.T) {
		err := validateResponseSchema(objectSchema, `{"other":"value"}`)
		require.Error(t, err, "response missing required field must fail validation")
		assert.Contains(t, err.Error(), "schema_mismatch", "error must identify schema_mismatch")
	})

	t.Run("non-JSON response returns schema_mismatch", func(t *testing.T) {
		err := validateResponseSchema(objectSchema, "not valid json {")
		require.Error(t, err, "non-JSON response must fail validation")
		assert.Contains(t, err.Error(), "schema_mismatch", "non-JSON error must identify schema_mismatch")
	})

	t.Run("wrong type returns schema_mismatch", func(t *testing.T) {
		stringSchema := `{"type":"string"}`
		err := validateResponseSchema(stringSchema, `{"object":"not a string"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "schema_mismatch")
	})

	t.Run("invalid stored schema returns schema_mismatch", func(t *testing.T) {
		err := validateResponseSchema(`{"type":"not_a_valid_type"}`, `"hello"`)
		require.Error(t, err, "invalid schema definition must produce an error")
		assert.Contains(t, err.Error(), "schema_mismatch")
	})

	t.Run("schema_mismatch error does not suggest send_message", func(t *testing.T) {
		err := validateResponseSchema(objectSchema, `{"wrong":"field"}`)
		require.Error(t, err)
		assert.False(t, strings.Contains(err.Error(), "send_message"),
			"schema_mismatch error must not reference send_message")
	})
}
