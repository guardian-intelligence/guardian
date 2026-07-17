package main

import "testing"

func TestOrganizationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		organizations map[string]organizationDetails
		want          string
		wantError     bool
	}{
		{
			name: "one active organization",
			organizations: map[string]organizationDetails{
				"guardian": {ID: "8ee8b358-71b7-4fd8-955a-936d26b725b1"},
			},
			want: "8ee8b358-71b7-4fd8-955a-936d26b725b1",
		},
		{name: "missing organization", wantError: true},
		{
			name: "ambiguous organizations",
			organizations: map[string]organizationDetails{
				"guardian": {ID: "8ee8b358-71b7-4fd8-955a-936d26b725b1"},
				"customer": {ID: "67856697-6f4a-4517-862b-84e3bd3f49e5"},
			},
			wantError: true,
		},
		{
			name: "empty organization id",
			organizations: map[string]organizationDetails{
				"guardian": {},
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := organizationID(test.organizations)
			if test.wantError {
				if err == nil {
					t.Fatalf("organizationID() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("organizationID() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("organizationID() = %q, want %q", got, test.want)
			}
		})
	}
}
