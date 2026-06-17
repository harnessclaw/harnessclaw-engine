// Package principles holds per-role principles text for the prompt builder.
//
// Each role lives in its own file (emma.go, scheduler.go, worker.go, …) so
// behaviour tuning for one role never forces you to scroll through the
// others. This file is the only public surface: the Role type and the
// Principles / PrinciplesCompact dispatch functions.
//
// Layer mapping:
//   - RoleEmma                                   → L1 (user-facing main agent)
//   - RoleWorker / Explore / Plan / Freelancer
//     / ContentCreator                           → L3 (sub-agents executing
//     tasks dispatched by emma)
package principles

// Role identifies which agent's principles to render.
type Role string

const (
	RoleEmma           Role = "emma"
	RoleWorker         Role = "worker"
	RoleExplore        Role = "explore"
	RolePlan           Role = "plan"
	RoleFreelancer     Role = "freelancer"
	RoleContentCreator Role = "content_creator"
)

// Principles returns the full principles text for the given role. Unknown
// roles fall back to RoleWorker (the safest generic executor profile).
//
// To add a new role:
//  1. Add a Role constant above
//  2. Add a new file `<role>.go` with the text constant
//  3. Add a case in this switch
//  4. (Optional) add a compact form in PrinciplesCompact
func Principles(role Role) string {
	switch role {
	case RoleEmma:
		return emmaPrinciples
	case RoleWorker:
		return workerPrinciples
	case RoleExplore:
		return explorePrinciples
	case RolePlan:
		return planPrinciples
	case RoleFreelancer:
		return freelancerPrinciples
	case RoleContentCreator:
		return contentCreatorPrinciples
	default:
		return workerPrinciples
	}
}

// PrinciplesCompact returns the budget-tight fallback for a role. The
// prompt builder uses it when the full text would not fit in the
// allocated section budget. Roles without a dedicated compact form return
// their full text — keep them short enough that this is acceptable.
func PrinciplesCompact(role Role) string {
	switch role {
	case RoleEmma:
		return emmaPrinciplesCompact
	default:
		return Principles(role)
	}
}
