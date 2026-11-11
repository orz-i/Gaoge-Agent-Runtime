package harnesspostgres

import (
	"errors"
	"strings"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// boundedInteractionAuthorityChildrenTx loads up to two *authoritative*
// siblings of one globally unique immutable source ID. All Turns are
// inspected: a historical receipt with a corrupted Turn must not disappear
// merely because a SQL filter assumed the claimed owner was trustworthy.
// The Resolved Interaction source allows ordinary non-Diagnostic Artifacts,
// but no additional Diagnostic or reserved Host response child. Receipt and
// continuation-claim parents allow exactly one child of any Kind.
// The caller holds its Turn's serializing transaction lock; no second proof
// ledger or write path is created.
func boundedInteractionAuthorityChildrenTx(
	tx *gorm.DB, parentID string, resolvedSource bool,
) ([]itemRecord, error) {
	query := tx.Where("parent_item_id = ?", parentID)
	if resolvedSource {
		query = query.Where("(kind = ? OR substr(id, 1, ?) = ?)",
			string(harness.ItemDiagnostic), len("hbizverified_"), "hbizverified_")
	}
	var children []itemRecord
	if err := query.Limit(2).Find(&children).Error; err != nil {
		return nil, err
	}
	return children, nil
}

// Called by AppendItem under the original Turn transaction lock. Reserved
// Host callback and continuation proof facts share the exact type-specific
// read predicate exported by Harness, rather than a SQL-only shortcut.
// This is a cold recovery/effect boundary; the Store fetches original
// immutable Turn Item facts rather than introducing a second proof ledger.
func checkInteractionAuthorityAppendTx(tx *gorm.DB, value harness.Item) error {
	interactionID, reserved := harness.InteractionAuthorityFactID(value)
	if !reserved {
		return nil
	}
	if interactionID == "" {
		return harness.ErrConflict
	}
	// A new original Continuation claim must not slip in after an
	// already-admitted cancellation intent. Both append paths own the
	// same original Turn row lock and use the shared typed fence proof.
	// Passive callback/completion observations may still be persisted
	// for read-only reconciliation while cancellation is outstanding.
	if strings.HasPrefix(value.ID, "hcontclaim_") {
		if err := checkCancellationFenceTx(tx, value.TurnID); err != nil {
			return err
		}
	}
	var interactionRecordValue interactionRecord
	if err := tx.Where("id = ?", interactionID).Take(&interactionRecordValue).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return harness.ErrConflict
		}
		return err
	}
	interaction, err := interactionFromRecord(interactionRecordValue)
	if err != nil {
		return err
	}
	var ownerRecord invocationRecord
	if err = tx.Where("id = ?", interaction.InvocationID).Take(&ownerRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return harness.ErrConflict
		}
		return err
	}
	owner, err := invocationFromRecord(ownerRecord)
	if err != nil {
		return err
	}
	// The original Waiting/Resolved Interaction Item is the root of this
	// Host proof chain, not a completion that needs prior ancestors. Bind
	// its entire immutable payload to the exact Store Interaction state
	// before allowing the source ID to enter the append-only ledger.
	if strings.HasPrefix(value.ID, "hinteractionitem_") {
		if !harness.VerifiedInteractionAuthorityAppend(value, interaction, owner, nil) {
			return harness.ErrConflict
		}
		return nil
	}
	// The three immutable Host proofs form a bounded source chain:
	// Resolved Interaction -> Host response -> Continuation claim -> Done.
	// Read those exact indexed IDs (at most three ancestors), not all
	// historical Items under the Turn lock. A long Turn must not turn one
	// legitimate callback into an unbounded JSON materialization.
	prior := make([]harness.Item, 0, 7)
	seen := make(map[string]struct{}, 7)
	var sourceID, responseID, claimID string
	parentID := value.ParentItemID
	for depth := 0; depth < 3 && parentID != ""; depth++ {
		if _, cycle := seen[parentID]; cycle {
			return harness.ErrConflict
		}
		parent, found, loadErr := findItem(tx, parentID)
		if loadErr != nil {
			return loadErr
		}
		if !found || parent.TurnID != value.TurnID || parent.Seq >= value.Seq {
			return harness.ErrConflict
		}
		seen[parent.ID] = struct{}{}
		prior = append(prior, parent)
		if strings.HasPrefix(parent.ID, "hbizverified_") {
			responseID = parent.ID
		}
		if strings.HasPrefix(parent.ID, "hcontclaim_") {
			claimID = parent.ID
		}
		if parent.Kind == harness.ItemInteraction {
			sourceID = parent.ID
			break
		}
		parentID = parent.ParentItemID
	}
	// Original non-canonical or additional children must fail closed on
	// restart. Read up to two children of each exact existing authority
	// fact: >1 already proves ambiguity. The Turn-lock transaction keeps
	// those sibling observations stable with competing AppendItem calls.
	for _, authorityID := range []string{sourceID, responseID, claimID} {
		if authorityID == "" {
			continue
		}
		children, readErr := boundedInteractionAuthorityChildrenTx(tx, authorityID, authorityID == sourceID)
		if readErr != nil {
			return readErr
		}
		for _, record := range children {
			if _, already := seen[record.ID]; already {
				continue
			}
			child, convertErr := itemFromRecord(record)
			if convertErr != nil {
				return convertErr
			}
			prior = append(prior, child)
			seen[child.ID] = struct{}{}
		}
	}
	if !harness.VerifiedInteractionAuthorityAppend(value, interaction, owner, prior) {
		return harness.ErrConflict
	}
	return nil
}
