package postgrestest

import "testing"

func TestValidateDatabaseName(t *testing.T) {
	tests := []struct {
		name     string
		database string
		wantErr  bool
	}{
		{name: "test suffix", database: "wzap_test", wantErr: false},
		{name: "test suffix with env", database: "wzap_staging_test", wantErr: false},
		{name: "production name", database: "wzap", wantErr: true},
		{name: "test inside name", database: "onefisc_test_wzap", wantErr: true},
		{name: "empty", database: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabaseName(tt.database)
			if tt.wantErr && err == nil {
				t.Fatalf("validateDatabaseName(%q) = nil, want error", tt.database)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateDatabaseName(%q) = %v, want nil", tt.database, err)
			}
		})
	}
}
