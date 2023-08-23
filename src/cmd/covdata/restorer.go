package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"

	"internal/coverage"
	"internal/coverage/decodecounter"
	"internal/coverage/decodemeta"
	"internal/coverage/pods"
)

func makeCleanerOp(bef covOperation, filename string) (covOperation, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	ds, ok := bef.(*dstate)
	if !ok {
		return nil, fmt.Errorf("The -restore flag can only be used with a debugdump option")
	}
	c := &restorer{
		op:             ds,
		content:        content,
		filename:       filename,
		fset:           token.NewFileSet(),
		coverableUnits: make(map[token.Pos][]coverage.CoverableUnit),
		upstream:       make(map[coverage.CoverableUnit][]coverage.CoverableUnit),
	}
	return c, nil
}

type restorer struct {
	op             *dstate
	content        []byte
	fset           *token.FileSet
	filename       string
	coverableUnits map[token.Pos][]coverage.CoverableUnit              // Stores coverableUnits inside a block
	upstream       map[coverage.CoverableUnit][]coverage.CoverableUnit // Stores coverable units that can be inferred from a certain unit
}

func (c *restorer) Setup() {
	if err := c.generateUpstreamMapping(); err != nil {
		fatal("generate upstream mapping: %v", err)
	}
	c.op.Setup()
}

func (c *restorer) Usage(msg string) {
	c.op.Usage(msg)
}

func (c *restorer) BeginPod(p pods.Pod) {
	c.op.BeginPod(p)
}

func (c *restorer) EndPod(p pods.Pod) {
	c.op.EndPod(p)
}

func (c *restorer) VisitMetaDataFile(mdf string, mfr *decodemeta.CoverageMetaFileReader) {
	c.op.VisitMetaDataFile(mdf, mfr)
}

func (c *restorer) BeginCounterDataFile(cdf string, cdr *decodecounter.CounterDataReader, dirIdx int) {
	c.op.BeginCounterDataFile(cdf, cdr, dirIdx)
}

func (c *restorer) EndCounterDataFile(cdf string, cdr *decodecounter.CounterDataReader, dirIdx int) {
	c.op.EndCounterDataFile(cdf, cdr, dirIdx)
}

func (c *restorer) VisitFuncCounterData(payload decodecounter.FuncPayload) {
	c.op.VisitFuncCounterData(payload)
}

func (c *restorer) EndCounters() {
	c.op.EndCounters()
}

func (c *restorer) BeginPackage(pd *decodemeta.CoverageMetaDataDecoder, pkgIdx uint32) {
	c.op.BeginPackage(pd, pkgIdx)
}

func (c *restorer) EndPackage(pd *decodemeta.CoverageMetaDataDecoder, pkgIdx uint32) {
	c.op.EndPackage(pd, pkgIdx)
}

func (c *restorer) VisitFunc(pkgIdx uint32, fnIdx uint32, fd *coverage.FuncDesc) {
	key := pkfunc{pk: pkgIdx, fcn: fnIdx}
	v, haveCounters := c.op.mm[key]
	if haveCounters {
		v.Counters = c.recoverCounters(v.Counters, fd)
		c.op.mm[key] = v
	}
	c.op.VisitFunc(pkgIdx, fnIdx, fd)
}

func (c *restorer) recoverCounters(counters []uint32, fd *coverage.FuncDesc) []uint32 {
	counterIdx := make(map[coverage.CoverableUnit]int)
	for i, unit := range fd.Units {
		counterIdx[unit] = i
	}
	var queue []int
	for i, val := range counters {
		if val != 0 {
			queue = append(queue, i)
		}
	}
	newCounters := make([]uint32, len(fd.Units))
	// BFS based idea to complete the counters' information based on the
	// relationship between coverable units
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		if newCounters[idx] == 1 {
			continue
		}
		newCounters[idx] = 1
		units, ok := c.upstream[fd.Units[idx]]
		if !ok {
			continue
		}
		for _, u := range units {
			queue = append(queue, counterIdx[u])
		}
	}
	return newCounters
}

func (c *restorer) Finish() {
	c.op.Finish()
}

func (c *restorer) endsBasicSourceBlock(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.BlockStmt:
		// Treat blocks like basic blocks to avoid overlapping counters.
		return true
	case *ast.IfStmt:
		return true
	case *ast.ForStmt:
		return true
	}
	return false
}

func (c *restorer) createCoverableUnit(start, end token.Pos, numStmt int) coverage.CoverableUnit {
	stpos := c.fset.Position(start)
	enpos := c.fset.Position(end)
	return coverage.CoverableUnit{
		StLine:  uint32(stpos.Line),
		StCol:   uint32(stpos.Column),
		EnLine:  uint32(enpos.Line),
		EnCol:   uint32(enpos.Column),
		NxStmts: uint32(numStmt),
	}
}

func (c *restorer) getCoverableUnits(stmt ast.Stmt) []coverage.CoverableUnit {
	switch n := stmt.(type) {
	case *ast.BlockStmt:
		return c.coverableUnits[n.Lbrace]
	case *ast.IfStmt:
		units := c.coverableUnits[n.Body.Lbrace]
		if n.Else != nil {
			units = append(units, c.getCoverableUnits(n.Else)...)
		}
		return units
	case *ast.ForStmt:
		return c.coverableUnits[n.Body.Lbrace]
	}
	return nil
}

func (c *restorer) statementBoundary(stmt ast.Stmt) token.Pos {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		return s.Body.Lbrace
	case *ast.BlockStmt:
		return s.Lbrace
	case *ast.ForStmt:
		return s.Body.Lbrace
	case *ast.LabeledStmt:
		return c.statementBoundary(s.Stmt)
	}
	return stmt.End()
}

// Copying this from go tool cover, to generate the exact same CoverableUnits
// This will return the coverable units per block and also connect the ones
// that can be inferred from others
func (c *restorer) generateCoverableUnits(pos, insertPos, blockEnd token.Pos, list []ast.Stmt, extendToClosingBrace bool) []coverage.CoverableUnit {
	var units []coverage.CoverableUnit
	// Special case: make sure we add a counter to an empty block. Can't do this below
	// or we will add a counter to an empty statement list after, say, a return statement.
	if len(list) == 0 {
		return []coverage.CoverableUnit{c.createCoverableUnit(insertPos, blockEnd, 0)}
	}
	// Make a copy of the list, as we may mutate it and should leave the
	// existing list intact.
	list = append([]ast.Stmt(nil), list...)
	// We have a block (statement list), but it may have several basic blocks due to the
	// appearance of statements that affect the flow of control.
	for {
		// Find first statement that affects flow of control (break, continue, if, etc.).
		// It will be the last statement of this basic block.
		var last int
		var providers []coverage.CoverableUnit
		end := blockEnd
		for last = 0; last < len(list); last++ {
			stmt := list[last]
			end = c.statementBoundary(stmt)

			if c.endsBasicSourceBlock(stmt) {
				// Traverse this stmt to generate its
				// coverable units
				ast.Walk(c, stmt)
				// Get all coverable units belonging to this
				// stmt
				providers = c.getCoverableUnits(stmt)
				last++
				extendToClosingBrace = false // Block is broken up now.
				break
			}
		}
		if extendToClosingBrace {
			end = blockEnd
		}
		if pos != end {
			unit := c.createCoverableUnit(pos, end, last)
			for _, u := range providers {
				c.upstream[u] = append(c.upstream[u], unit)
			}
			if len(units) != 0 {
				c.upstream[unit] = append(c.upstream[unit], units[len(units)-1])
			}
			units = append(units, unit)
		}
		list = list[last:]
		if len(list) == 0 {
			break
		}
		pos = list[0].Pos()
		insertPos = pos
	}
	return units
}

func (c *restorer) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.BlockStmt:
		c.coverableUnits[n.Lbrace] = c.generateCoverableUnits(n.Lbrace, n.Lbrace+1, n.Rbrace+1, n.List, true)
		return nil
	case *ast.IfStmt:
		ast.Walk(c, n.Body)
		if n.Else != nil {
			ast.Walk(c, n.Else)
		}
		return nil
	case *ast.ForStmt:
		ast.Walk(c, n.Body)
		return nil
	case *ast.FuncDecl:
		ast.Walk(c, n.Body)
		return nil
	}
	return c
}

func (c *restorer) generateUpstreamMapping() error {
	f, err := parser.ParseFile(c.fset, c.filename, c.content, parser.AllErrors|parser.ParseComments)
	if err != nil {
		return err
	}
	ast.Walk(c, f)
	return nil
}
