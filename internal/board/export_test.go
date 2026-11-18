package board

import "context"

// AddPlanRowForTest is addPlanRow, for the tests of what it checks under the project's lock: the
// way in through AddAmendment looks first without it, and two callers rarely pass that look together.
func (b *Board) AddPlanRowForTest(ctx context.Context, ref, kind, text, who string) error {
	p, err := b.Project(ctx, ref)
	if err != nil {
		return err
	}
	return b.addPlanRow(ctx, p, kind, text, who)
}

// ReviewCoversHeadForTest is the check review_covers_head, for the tests of its refusals.
var ReviewCoversHeadForTest = reviewCoversHead

// PackagePathsForTest is packagePaths, for the test of how a waiting reason names package files.
var PackagePathsForTest = packagePaths

// CodeChangesAtForTest is codeChangesAt, for the test of an index file past the bound of a read.
var CodeChangesAtForTest = codeChangesAt

// InstructionsInForTest is instructionsIn, for the same test.
func (b *Board) InstructionsInForTest(ctx context.Context, p *Project, branch, fetched string) ([]string, error) {
	return b.instructionsIn(ctx, p, branch, fetched)
}

// SkillsVendoredForTest is the check skills_vendored, for the test of its error on a way out.
var SkillsVendoredForTest = skillsVendored
