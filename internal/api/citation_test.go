package api

import "testing"

func TestValidateCitationReferences(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		count   int
		wantErr bool
	}{
		{name: "valid", answer: "事实 [S1]", count: 1},
		{name: "missing", answer: "没有引用", count: 1, wantErr: true},
		{name: "out of range", answer: "事实 [S2]", count: 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCitationReferences(test.answer, test.count); (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}
