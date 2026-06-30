package openbao

import (
	"testing"

	baoapi "github.com/openbao/openbao/api/v2"
)

func TestIsOpenBaoPKIIssuerNotFound(t *testing.T) {
	err := &baoapi.ResponseError{
		StatusCode: 500,
		Errors: []string{
			"1 error occurred:\n\t* unable to find PKI issuer for reference: openbao-api-root-2026\n\n",
		},
	}
	if !isOpenBaoPKIIssuerNotFound(err) {
		t.Fatalf("isOpenBaoPKIIssuerNotFound() = false, want true")
	}
}

func TestIsOpenBaoPKIIssuerNotFoundRejectsOther500(t *testing.T) {
	err := &baoapi.ResponseError{
		StatusCode: 500,
		Errors:     []string{"storage backend unavailable"},
	}
	if isOpenBaoPKIIssuerNotFound(err) {
		t.Fatalf("isOpenBaoPKIIssuerNotFound() = true, want false")
	}
}
