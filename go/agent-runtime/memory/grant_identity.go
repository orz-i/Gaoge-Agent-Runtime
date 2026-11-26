package memory

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"

func (s *Store) grantIdentityUsedLocked(id string) bool {
	_, occupied := s.grantIdentities[id]
	_, active := s.childGrants[id]
	_, retired := s.ownedRetirements[id]
	return occupied || active || retired
}

func (s *Store) reserveGrantIdentityLocked(g kernel.ChildExecutionGrant) error {
	i := g.Identity()
	if !i.Valid() {
		return kernel.ErrInvalidInput
	}
	if s.grantIdentityUsedLocked(i.GrantID) {
		return kernel.ErrExecutionConflict
	}
	if s.grantIdentities == nil {
		s.grantIdentities = make(map[string]string)
	}
	s.grantIdentities[i.GrantID] = i.RunID
	return nil
}
