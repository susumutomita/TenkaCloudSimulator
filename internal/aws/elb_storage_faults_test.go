package aws

import (
	"context"
	"database/sql"
	"testing"
)

func TestELBStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	allow := func(string, string) error { return nil }
	lbInput := ELBLoadBalancer{Name: "faults", Region: "us-east-1", Scheme: "internet-facing", Subnets: []string{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1"}, SecurityGroups: []string{"sg-0123456789abcdef0"}}
	tgInput := ELBTargetGroup{Name: "faults-tg", Region: "us-east-1", Health: ELBHealthCheck{Enabled: true, Path: "/healthz", Matcher: "200", Interval: 35, Timeout: 30, Healthy: 5, Unhealthy: 2}}
	type graph struct {
		s  *ELBStore
		lb ELBLoadBalancer
		tg ELBTargetGroup
		ls ELBListener
	}
	build := func(t *testing.T, db *sql.DB, depth int) graph {
		t.Helper()
		s, err := NewELBStore(db)
		if err != nil {
			t.Fatal(err)
		}
		g := graph{s: s}
		ctx := context.Background()
		if depth > 0 {
			if g.lb, err = s.CreateLoadBalancer(ctx, lbInput, allow); err != nil {
				t.Fatal(err)
			}
		}
		if depth > 1 {
			if g.tg, err = s.CreateTargetGroup(ctx, tgInput, allow); err != nil {
				t.Fatal(err)
			}
		}
		if depth > 2 {
			if g.ls, err = s.CreateListener(ctx, ELBListener{LoadBalancerARN: g.lb.ARN, Region: "us-east-1", Protocol: "HTTP", Port: 80, Action: ELBAction{Type: "forward", TargetGroupARN: g.tg.ARN}}, allow); err != nil {
				t.Fatal(err)
			}
		}
		return g
	}
	steps := []struct {
		name  string
		depth int
		op    func(ctx context.Context, g graph) error
	}{
		{"create load balancer", 0, func(ctx context.Context, g graph) error {
			_, err := g.s.CreateLoadBalancer(ctx, lbInput, allow)
			return err
		}},
		{"create target group", 1, func(ctx context.Context, g graph) error {
			_, err := g.s.CreateTargetGroup(ctx, tgInput, allow)
			return err
		}},
		{"create forwarding listener", 2, func(ctx context.Context, g graph) error {
			_, err := g.s.CreateListener(ctx, ELBListener{LoadBalancerARN: g.lb.ARN, Region: "us-east-1", Protocol: "HTTP", Port: 80, Action: ELBAction{Type: "forward", TargetGroupARN: g.tg.ARN}}, allow)
			return err
		}},
		{"modify target group health", 3, func(ctx context.Context, g graph) error {
			_, err := g.s.ModifyTargetGroup(ctx, "us-east-1", g.tg.ARN, func(h ELBHealthCheck) (ELBHealthCheck, error) {
				h.Path = "/ready"
				return h, nil
			}, allow)
			return err
		}},
		{"describe graph", 3, func(ctx context.Context, g graph) error {
			if lbs, err := g.s.DescribeLoadBalancers(ctx, "us-east-1", nil, nil); err != nil {
				return err
			} else if len(lbs) != 1 {
				t.Fatalf("load balancers %v", lbs)
			}
			if tgs, err := g.s.DescribeTargetGroups(ctx, "us-east-1", g.lb.ARN, nil, nil); err != nil {
				return err
			} else if len(tgs) != 1 {
				t.Fatalf("target groups %v", tgs)
			}
			if ls, err := g.s.DescribeListeners(ctx, "us-east-1", g.lb.ARN, nil); err != nil {
				return err
			} else if len(ls) != 1 || ls[0].Port != 80 {
				t.Fatalf("listeners %v", ls)
			}
			_, err := g.s.DescribeTargetHealth(ctx, "us-east-1", g.tg.ARN, nil)
			return err
		}},
		{"delete listener", 3, func(ctx context.Context, g graph) error { return g.s.DeleteListener(ctx, "us-east-1", g.ls.ARN, allow) }},
		{"delete load balancer with listener", 3, func(ctx context.Context, g graph) error {
			return g.s.DeleteLoadBalancer(ctx, "us-east-1", g.lb.ARN, allow)
		}},
		{"delete target group", 2, func(ctx context.Context, g graph) error {
			return g.s.DeleteTargetGroup(ctx, "us-east-1", g.tg.ARN, allow)
		}},
	}
	for _, step := range steps {
		step := step
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
				g := build(t, db, step.depth)
				return func(ctx context.Context) error { return step.op(ctx, g) }
			})
		})
	}
}
