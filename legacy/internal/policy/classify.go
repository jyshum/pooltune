package policy

import (
	"strconv"
	"time"

	"pooltune/internal/domain"
)

// Classify performs bounded exact lookups. The ingress adapter owns identity
// verification and must reject authentication/protocol errors before calling it.
func Classify(c *CompiledPolicy, ctx domain.RequestContext) domain.Classification {
	return c.classifyAt(ctx, time.Now())
}

func (c *CompiledPolicy) classifyAt(ctx domain.RequestContext, now time.Time) domain.Classification {
	if c == nil {
		return domain.Classification{Class: domain.ClassNormal, Workload: "blocked", FairnessKey: "overflow", Blocked: true, QueueDeadline: now}
	}
	key := Selector{Target: ctx.Target, Path: ctx.Path, Principal: ctx.Principal, Method: ctx.Method, Route: ctx.Route}
	v, ok := c.exact[key]
	if !ok {
		v, ok = c.defaults[Selector{Target: ctx.Target, Path: ctx.Path, Principal: ctx.Principal}]
	}
	if !ok {
		return fallback(ctx, now, c.revision, c.fallback)
	}
	deadline := now.Add(v.queueWait)
	if !ctx.ClientDeadline.IsZero() && ctx.ClientDeadline.Before(deadline) {
		deadline = ctx.ClientDeadline
	}
	fairness := fairKey(ctx.Target, ctx.Path, ctx.Principal, "")
	if v.tenantTrust && ctx.TrustedTenant && identifier(ctx.Tenant) {
		fairness = fairKey(ctx.Target, ctx.Path, ctx.Principal, ctx.Tenant)
	}
	return domain.Classification{Class: v.class, Workload: v.id, FairnessKey: fairness, RuleID: v.ruleID, Revision: c.revision, QueueDeadline: deadline, ConcurrencyCap: v.concurrencyCap, QueueLimit: v.queueLimit, Blocked: v.blocked}
}

func fallback(ctx domain.RequestContext, now time.Time, revision domain.RevisionID, limit Fallback) domain.Classification {
	deadline := now.Add(limit.QueueWait)
	if !ctx.ClientDeadline.IsZero() && ctx.ClientDeadline.Before(deadline) {
		deadline = ctx.ClientDeadline
	}
	return domain.Classification{Class: domain.ClassNormal, Workload: "overflow", FairnessKey: "overflow", Revision: revision, QueueDeadline: deadline, ConcurrencyCap: limit.ConcurrencyCap, QueueLimit: limit.QueueLimit}
}

func fairKey(target string, path domain.PathKind, principal, tenant string) string {
	// Length prefixes prevent distinct components from sharing a key.
	return strconv.Itoa(len(target)) + ":" + target + strconv.Itoa(len(path)) + ":" + string(path) + strconv.Itoa(len(principal)) + ":" + principal + strconv.Itoa(len(tenant)) + ":" + tenant
}

// Reclassify retains trusted workload/fairness identity while applying the stable
// policy's current settings. It is used only for queued rollback, never active work.
func (c *CompiledPolicy) Reclassify(old domain.Classification) (domain.Classification, error) {
	if c == nil {
		return domain.Classification{}, ErrBoundary
	}
	out := old
	out.Revision = c.revision
	if w, ok := c.byID[old.Workload]; ok {
		out.Class = w.class
		out.RuleID = w.ruleID
		out.ConcurrencyCap = w.concurrencyCap
		out.QueueLimit = w.queueLimit
		out.Blocked = w.blocked
		if deadline := time.Now().Add(w.queueWait); deadline.Before(out.QueueDeadline) {
			out.QueueDeadline = deadline
		}
		return out, nil
	}
	if old.Workload != "overflow" {
		return domain.Classification{}, ErrBoundary
	}
	out.Class = domain.ClassNormal
	out.RuleID = ""
	out.ConcurrencyCap = c.fallback.ConcurrencyCap
	out.QueueLimit = c.fallback.QueueLimit
	out.Blocked = false
	if deadline := time.Now().Add(c.fallback.QueueWait); deadline.Before(out.QueueDeadline) {
		out.QueueDeadline = deadline
	}
	return out, nil
}
