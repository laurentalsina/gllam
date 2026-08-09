package engine

import (
	"context"
	"testing"
)

func TestFastDownwardPlanner(t *testing.T) {
	planner := NewFastDownwardPlanner("/home/laurent/Projects/downward/fast-downward.py")

	domain := `(define (domain test-domain)
  (:requirements :strips)
  (:predicates (at ?x) (connected ?x ?y))
  (:action move
    :parameters (?from ?to)
    :precondition (and (at ?from) (connected ?from ?to))
    :effect (and (not (at ?from)) (at ?to)))
)`

	problem := `(define (problem test-prob)
  (:domain test-domain)
  (:objects loc1 loc2 - object)
  (:init (at loc1) (connected loc1 loc2))
  (:goal (at loc2))
)`

	ctx := context.Background()
	actions, err := planner.Solve(ctx, domain, problem)
	if err != nil {
		t.Fatalf("FastDownwardPlanner failed: %v", err)
	}

	if len(actions) == 0 {
		t.Fatalf("Expected non-empty plan, got 0 actions")
	}

	t.Logf("Successfully solved plan with %d actions: %v", len(actions), actions)
}
