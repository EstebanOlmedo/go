package main

import (
	"fmt"
	"go/token"
	"os"
	"reflect"
	"testing"

	"internal/coverage"
)

func TestGenerateUpstreamMapping(t *testing.T) {
	testdata := []struct {
		srcPath string
		want    map[coverage.CoverableUnit][]coverage.CoverableUnit
	}{
		{
			srcPath: "testdata/inf1.go",
			want: map[coverage.CoverableUnit][]coverage.CoverableUnit{
				{
					StLine: 7, StCol: 22,
					EnLine: 9, EnCol: 3,
					NxStmts: 1,
				}: {
					{
						StLine: 5, StCol: 13,
						EnLine: 7, EnCol: 22,
						NxStmts: 2,
					},
				},
				{
					StLine: 10, StCol: 2,
					EnLine: 10, EnCol: 18,
					NxStmts: 1,
				}: {
					{
						StLine: 5, StCol: 13,
						EnLine: 7, EnCol: 22,
						NxStmts: 2,
					},
				},
			},
		},
	}
	for _, tt := range testdata {
		content, err := os.ReadFile(tt.srcPath)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		c := &restorer{
			filename:       tt.srcPath,
			fset:           token.NewFileSet(),
			content:        content,
			upstream:       make(map[coverage.CoverableUnit][]coverage.CoverableUnit),
			coverableUnits: make(map[token.Pos][]coverage.CoverableUnit),
		}
		err = c.generateUpstreamMapping()
		if err != nil {
			t.Fatalf("generating upstream mapping: %v", err)
		}
		if !reflect.DeepEqual(tt.want, c.upstream) {
			fmt.Println("\twant: ", tt.want)
			fmt.Println("\thave: ", c.upstream)
			t.FailNow()
		}
	}
}
