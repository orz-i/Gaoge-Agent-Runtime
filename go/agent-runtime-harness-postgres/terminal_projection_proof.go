package harnesspostgres

import (
	"context"
	"errors"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// checkTerminalProjectionTx runs under the original Turn's UPDATE lock,
// shared by every Host continuation claim Appendix and the Invocation/Turn
// status CAS. No uncommitted physical Resume may be silently terminalized
// by an independent Kernel snapshot or a stale public Refresh.
//
// Only exact Host source/receipt/claim/done IDs and their children are read,
// instead of loading all historic Items under the Turn transaction lock.
func checkTerminalProjectionTx(
	ctx context.Context, tx *gorm.DB, turnID, invocationID string,
) error {
	var records []interactionRecord
	// Waiting and malformed original Interactions must also be visible to
	// the shared terminal predicate. Filtering to Resolved would turn an
	// unresolved Host request into a falsely absent authority.
	query := tx.Where("turn_id = ?", turnID)
	if invocationID != "" {
		query = query.Where("invocation_id = ?", invocationID)
	}
	if err := query.Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	interactions := make([]harness.Interaction, 0, len(records))
	owners := make([]harness.Invocation, 0, len(records))
	factIDs := make([]string, 0, 4*len(records))
	parentIDs := make([]string, 0, 3*len(records))
	resolvedSourceIDs := make(map[string]bool, len(records))
	for _, record := range records {
		interaction, err := interactionFromRecord(record)
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
		if err != nil || owner.TurnID != turnID {
			return errors.Join(harness.ErrConflict, err)
		}
		ids, parentRefs := harness.TerminalProjectionProofIDs(interaction.ID)
		factIDs = append(factIDs, ids...)
		parentIDs = append(parentIDs, parentRefs...)
		if len(parentRefs) > 0 {
			resolvedSourceIDs[parentRefs[0]] = true
		}
		interactions = append(interactions, interaction)
		owners = append(owners, owner)
	}
	// Exact global IDs are primary-key lookups. Siblings must be checked
	// by original parent identity across all Turns: the persisted Turn ID is
	// an attribute to verify, not a trustworthy pre-verification filter.
	// Bound each source to at most two relevant children, rather than
	// materializing every Artifact attachment under a long-lived Turn.
	var proofRows []itemRecord
	if err := tx.Where("id IN ?", factIDs).Find(&proofRows).Error; err != nil {
		return err
	}
	for _, parentID := range parentIDs {
		children, readErr := boundedInteractionAuthorityChildrenTx(tx, parentID, resolvedSourceIDs[parentID])
		if readErr != nil {
			return readErr
		}
		proofRows = append(proofRows, children...)
	}
	proofItems := make([]harness.Item, 0, len(proofRows))
	seen := make(map[string]struct{}, len(proofRows))
	for _, row := range proofRows {
		// A canonical receipt can appear in both the exact-ID lookup and
		// its original parent's sibling query. Count each immutable Item
		// once; an alternate ID or parent remains a separate fact.
		if _, repeated := seen[row.ID]; repeated {
			continue
		}
		seen[row.ID] = struct{}{}
		item, err := itemFromRecord(row)
		if err != nil {
			return err
		}
		proofItems = append(proofItems, item)
	}
	if !harness.ValidTerminalProjectionProof(
		ctx, turnID, invocationID, interactions, owners, proofItems,
	) {
		return harness.ErrConflict
	}
	return nil
}
