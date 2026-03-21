//go:build e2e

package e2e

import (
	"context"
	"testing"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
	"github.com/google/uuid"
)

func TestInstructionCRUDAndResolve(t *testing.T) {
	// NOTE: This test cannot run in parallel because it lists ALL instructions
	// in the system and expects a specific count. Other tests create org-level
	// instructions that would interfere with the assertions here.

	// Create 3 instructions
	_, err := clients.Instruction.CreateInstruction(context.Background(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:    "Instruction A",
			Content: "Review all Go files for proper error handling",
		}))
	if err != nil {
		t.Fatalf("CreateInstruction A: %v", err)
	}

	repoID := uuid.New().String()
	otherRepoID := uuid.New().String()

	instB, err := clients.Instruction.CreateInstruction(context.Background(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:              "Instruction B",
			Content:           "Check Go code for security issues",
			RepoFilter:        []string{repoID},
			FilePatternFilter: []string{"*.go"},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction B: %v", err)
	}
	instBID := instB.Msg.Instruction.Id

	_, err = clients.Instruction.CreateInstruction(context.Background(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:       "Instruction C",
			Content:    "Review everything in other repo",
			RepoFilter: []string{otherRepoID},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction C: %v", err)
	}

	// List instructions -> expect 3
	listResp, err := clients.Instruction.ListInstructions(context.Background(),
		connect.NewRequest(&apiv1.ListInstructionsRequest{}))
	if err != nil {
		t.Fatalf("ListInstructions: %v", err)
	}
	if len(listResp.Msg.Instructions) != 3 {
		t.Errorf("expected 3 instructions, got %d", len(listResp.Msg.Instructions))
	}

	// Update instruction A: change content, disable it
	instA := listResp.Msg.Instructions[0]
	_, err = clients.Instruction.UpdateInstruction(context.Background(),
		connect.NewRequest(&apiv1.UpdateInstructionRequest{
			Id:      instA.Id,
			Name:    instA.Name,
			Content: "Updated content",
			Enabled: false,
		}))
	if err != nil {
		t.Fatalf("UpdateInstruction: %v", err)
	}

	// List again -> instruction A shows updated content + disabled
	listResp, err = clients.Instruction.ListInstructions(context.Background(),
		connect.NewRequest(&apiv1.ListInstructionsRequest{}))
	if err != nil {
		t.Fatalf("ListInstructions (2): %v", err)
	}
	var foundA bool
	for _, i := range listResp.Msg.Instructions {
		if i.Id == instA.Id {
			foundA = true
			if i.Content != "Updated content" {
				t.Errorf("expected updated content, got %q", i.Content)
			}
			if i.Enabled {
				t.Errorf("expected instruction A to be disabled")
			}
		}
	}
	if !foundA {
		t.Errorf("instruction A not found in list")
	}

	// ResolveInstructions(repo_id=repoID, changed_files=["main.go", "README.md"])
	// -> expects only instruction B (A is disabled, C is wrong repo)
	resolveResp, err := clients.Instruction.ResolveInstructions(context.Background(),
		connect.NewRequest(&apiv1.ResolveInstructionsRequest{
			RepoId:       repoID,
			ChangedFiles: []string{"main.go", "README.md"},
		}))
	if err != nil {
		t.Fatalf("ResolveInstructions: %v", err)
	}
	if len(resolveResp.Msg.Instructions) != 1 {
		t.Errorf("expected 1 instruction, got %d", len(resolveResp.Msg.Instructions))
	} else if resolveResp.Msg.Instructions[0].Id != instBID {
		t.Errorf("expected instruction B, got %s", resolveResp.Msg.Instructions[0].Id)
	}

	// ResolveInstructions(repo_id=repoID, changed_files=["README.md"])
	// -> expects empty (B requires *.go files)
	resolveResp, err = clients.Instruction.ResolveInstructions(context.Background(),
		connect.NewRequest(&apiv1.ResolveInstructionsRequest{
			RepoId:       repoID,
			ChangedFiles: []string{"README.md"},
		}))
	if err != nil {
		t.Fatalf("ResolveInstructions (2): %v", err)
	}
	if len(resolveResp.Msg.Instructions) != 0 {
		t.Errorf("expected 0 instructions for README.md, got %d", len(resolveResp.Msg.Instructions))
	}

	// Delete instruction C
	_, err = clients.Instruction.DeleteInstruction(context.Background(),
		connect.NewRequest(&apiv1.DeleteInstructionRequest{
			Id: listResp.Msg.Instructions[2].Id,
		}))
	if err != nil {
		t.Fatalf("DeleteInstruction: %v", err)
	}

	// List -> expects 2
	listResp, err = clients.Instruction.ListInstructions(context.Background(),
		connect.NewRequest(&apiv1.ListInstructionsRequest{}))
	if err != nil {
		t.Fatalf("ListInstructions (3): %v", err)
	}
	if len(listResp.Msg.Instructions) != 2 {
		t.Errorf("expected 2 instructions after delete, got %d", len(listResp.Msg.Instructions))
	}
}
