package bug

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MichaelMure/git-bug/repository"
	"github.com/MichaelMure/git-bug/util"
)

const bugsRefPattern = "refs/bugeeeee/"
const bugsRemoteRefPattern = "refs/remotes/%s/bugs/"

const opsEntryName = "ops"
const rootEntryName = "root"
const mediaEntryName = "media"

const createClockEntryPrefix = "create-clock-"
const createClockEntryPattern = "create-clock-%d"
const editClockEntryPrefix = "edit-clock-"
const editClockEntryPattern = "edit-clock-%d"

const idLength = 40
const humanIdLength = 7

// Bug hold the data of a bug thread, organized in a way close to
// how it will be persisted inside Git. This is the data structure
// used to merge two different version of the same Bug.
type Bug struct {

	// A Lamport clock is a logical clock that allow to order event
	// inside a distributed system.
	// It must be the first field in this struct due to https://github.com/golang/go/issues/599
	createTime util.LamportTime
	editTime   util.LamportTime

	// Id used as unique identifier
	id string

	lastCommit util.Hash
	rootPack   util.Hash

	// all the committed operations
	packs []OperationPack

	// a temporary pack of operations used for convenience to pile up new operations
	// before a commit
	staging OperationPack
}

// NewBug create a new Bug
func NewBug() *Bug {
	// No id yet
	// No logical clock yet
	return &Bug{}
}

// FindLocalBug find an existing Bug matching a prefix
func FindLocalBug(repo repository.Repo, prefix string) (*Bug, error) {
	ids, err := repo.ListIds(bugsRefPattern)

	if err != nil {
		return nil, err
	}

	// preallocate but empty
	matching := make([]string, 0, 5)

	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			matching = append(matching, id)
		}
	}

	if len(matching) == 0 {
		return nil, errors.New("No matching bug found.")
	}

	if len(matching) > 1 {
		return nil, fmt.Errorf("Multiple matching bug found:\n%s", strings.Join(matching, "\n"))
	}

	return ReadLocalBug(repo, matching[0])
}

// ReadLocalBug will read a local bug from its hash
func ReadLocalBug(repo repository.Repo, id string) (*Bug, error) {
	ref := bugsRefPattern + id
	return readBug(repo, ref)
}

// ReadRemoteBug will read a remote bug from its hash
func ReadRemoteBug(repo repository.Repo, remote string, id string) (*Bug, error) {
	ref := fmt.Sprintf(bugsRemoteRefPattern, remote) + id
	return readBug(repo, ref)
}

// readBug will read and parse a Bug from git
func readBug(repo repository.Repo, ref string) (*Bug, error) {
	hashes, err := repo.ListCommits(ref)

	if err != nil {
		return nil, err
	}

	refSplitted := strings.Split(ref, "/")
	id := refSplitted[len(refSplitted)-1]

	if len(id) != idLength {
		return nil, fmt.Errorf("Invalid ref length")
	}

	bug := Bug{
		id: id,
	}

	// Load each OperationPack
	for _, hash := range hashes {
		entries, err := repo.ListEntries(hash)

		bug.lastCommit = hash

		if err != nil {
			return nil, err
		}

		var opsEntry repository.TreeEntry
		opsFound := false
		var rootEntry repository.TreeEntry
		rootFound := false
		var createTime uint64
		var editTime uint64

		for _, entry := range entries {
			if entry.Name == opsEntryName {
				opsEntry = entry
				opsFound = true
				continue
			}
			if entry.Name == rootEntryName {
				rootEntry = entry
				rootFound = true
			}
			if strings.HasPrefix(entry.Name, createClockEntryPrefix) {
				n, err := fmt.Sscanf(string(entry.Name), createClockEntryPattern, &createTime)
				if err != nil {
					return nil, err
				}
				if n != 1 {
					return nil, fmt.Errorf("could not parse create time lamport value")
				}
			}
			if strings.HasPrefix(entry.Name, editClockEntryPrefix) {
				n, err := fmt.Sscanf(string(entry.Name), editClockEntryPattern, &editTime)
				if err != nil {
					return nil, err
				}
				if n != 1 {
					return nil, fmt.Errorf("could not parse edit time lamport value")
				}
			}
		}

		if !opsFound {
			return nil, errors.New("Invalid tree, missing the ops entry")
		}
		if !rootFound {
			return nil, errors.New("Invalid tree, missing the root entry")
		}

		if bug.rootPack == "" {
			bug.rootPack = rootEntry.Hash
			bug.createTime = util.LamportTime(createTime)
		}

		bug.editTime = util.LamportTime(editTime)

		// Update the clocks
		if err := repo.CreateWitness(bug.createTime); err != nil {
			return nil, err
		}
		if err := repo.EditWitness(bug.editTime); err != nil {
			return nil, err
		}

		data, err := repo.ReadData(opsEntry.Hash)

		if err != nil {
			return nil, err
		}

		op, err := ParseOperationPack(data)

		if err != nil {
			return nil, err
		}

		// tag the pack with the commit hash
		op.commitHash = hash

		if err != nil {
			return nil, err
		}

		bug.packs = append(bug.packs, *op)
	}

	return &bug, nil
}

type StreamedBug struct {
	Bug *Bug
	Err error
}

// ReadAllLocalBugs read and parse all local bugs
func ReadAllLocalBugs(repo repository.Repo) <-chan StreamedBug {
	return readAllBugs(repo, bugsRefPattern)
}

// ReadAllRemoteBugs read and parse all remote bugs for a given remote
func ReadAllRemoteBugs(repo repository.Repo, remote string) <-chan StreamedBug {
	refPrefix := fmt.Sprintf(bugsRemoteRefPattern, remote)
	return readAllBugs(repo, refPrefix)
}

// Read and parse all available bug with a given ref prefix
func readAllBugs(repo repository.Repo, refPrefix string) <-chan StreamedBug {
	out := make(chan StreamedBug)

	go func() {
		defer close(out)

		refs, err := repo.ListRefs(refPrefix)
		if err != nil {
			out <- StreamedBug{Err: err}
			return
		}

		for _, ref := range refs {
			b, err := readBug(repo, ref)

			if err != nil {
				out <- StreamedBug{Err: err}
				return
			}

			out <- StreamedBug{Bug: b}
		}
	}()

	return out
}

// ListLocalIds list all the available local bug ids
func ListLocalIds(repo repository.Repo) ([]string, error) {
	return repo.ListIds(bugsRefPattern)
}

// IsValid check if the Bug data is valid
func (bug *Bug) IsValid() bool {
	// non-empty
	if len(bug.packs) == 0 && bug.staging.IsEmpty() {
		return false
	}

	// check if each pack is valid
	for _, pack := range bug.packs {
		if !pack.IsValid() {
			return false
		}
	}

	// check if staging is valid if needed
	if !bug.staging.IsEmpty() {
		if !bug.staging.IsValid() {
			return false
		}
	}

	// The very first Op should be a CreateOp
	firstOp := bug.FirstOp()
	if firstOp == nil || firstOp.OpType() != CreateOp {
		return false
	}

	// Check that there is no more CreateOp op
	it := NewOperationIterator(bug)
	createCount := 0
	for it.Next() {
		if it.Value().OpType() == CreateOp {
			createCount++
		}
	}

	if createCount != 1 {
		return false
	}

	return true
}

// Append an operation into the staging area, to be committed later
func (bug *Bug) Append(op Operation) {
	bug.staging.Append(op)
}

// HasPendingOp tell if the bug need to be committed
func (bug *Bug) HasPendingOp() bool {
	return !bug.staging.IsEmpty()
}

// Commit write the staging area in Git and move the operations to the packs
func (bug *Bug) Commit(repo repository.Repo) error {
	if bug.staging.IsEmpty() {
		return fmt.Errorf("can't commit a bug with no pending operation")
	}

	// Write the Ops as a Git blob containing the serialized array
	hash, err := bug.staging.Write(repo)
	if err != nil {
		return err
	}

	if bug.rootPack == "" {
		bug.rootPack = hash
	}

	// Make a Git tree referencing this blob
	tree := []repository.TreeEntry{
		// the last pack of ops
		{ObjectType: repository.Blob, Hash: hash, Name: opsEntryName},
		// always the first pack of ops (might be the same)
		{ObjectType: repository.Blob, Hash: bug.rootPack, Name: rootEntryName},
	}

	// Reference, if any, all the files required by the ops
	// Git will check that they actually exist in the storage and will make sure
	// to push/pull them as needed.
	mediaTree := makeMediaTree(bug.staging)
	if len(mediaTree) > 0 {
		mediaTreeHash, err := repo.StoreTree(mediaTree)
		if err != nil {
			return err
		}
		tree = append(tree, repository.TreeEntry{
			ObjectType: repository.Tree,
			Hash:       mediaTreeHash,
			Name:       mediaEntryName,
		})
	}

	// Store the logical clocks as well
	// --> edit clock for each OperationPack/commits
	// --> create clock only for the first OperationPack/commits
	//
	// To avoid having one blob for each clock value, clocks are serialized
	// directly into the entry name
	emptyBlobHash, err := repo.StoreData([]byte{})
	if err != nil {
		return err
	}

	editTime, err := repo.EditTimeIncrement()
	if err != nil {
		return err
	}

	tree = append(tree, repository.TreeEntry{
		ObjectType: repository.Blob,
		Hash:       emptyBlobHash,
		Name:       fmt.Sprintf(editClockEntryPattern, editTime),
	})
	if bug.lastCommit == "" {
		createTime, err := repo.CreateTimeIncrement()
		if err != nil {
			return err
		}

		tree = append(tree, repository.TreeEntry{
			ObjectType: repository.Blob,
			Hash:       emptyBlobHash,
			Name:       fmt.Sprintf(createClockEntryPattern, createTime),
		})
	}

	// Store the tree
	hash, err = repo.StoreTree(tree)
	if err != nil {
		return err
	}

	// Write a Git commit referencing the tree, with the previous commit as parent
	if bug.lastCommit != "" {
		hash, err = repo.StoreCommitWithParent(hash, bug.lastCommit)
	} else {
		hash, err = repo.StoreCommit(hash)
	}

	if err != nil {
		return err
	}

	bug.lastCommit = hash

	// if it was the first commit, use the commit hash as bug id
	if bug.id == "" {
		bug.id = string(hash)
	}

	// Create or update the Git reference for this bug
	// When pushing later, the remote will ensure that this ref update
	// is fast-forward, that is no data has been overwritten
	ref := fmt.Sprintf("%s%s", bugsRefPattern, bug.id)
	err = repo.UpdateRef(ref, hash)

	if err != nil {
		return err
	}

	bug.packs = append(bug.packs, bug.staging)
	bug.staging = OperationPack{}

	return nil
}

func makeMediaTree(pack OperationPack) []repository.TreeEntry {
	var tree []repository.TreeEntry
	counter := 0
	added := make(map[util.Hash]interface{})

	for _, ops := range pack.Operations {
		for _, file := range ops.Files() {
			if _, has := added[file]; !has {
				tree = append(tree, repository.TreeEntry{
					ObjectType: repository.Blob,
					Hash:       file,
					// The name is not important here, we only need to
					// reference the blob.
					Name: fmt.Sprintf("file%d", counter),
				})
				counter++
				added[file] = struct{}{}
			}
		}
	}

	return tree
}

// Merge a different version of the same bug by rebasing operations of this bug
// that are not present in the other on top of the chain of operations of the
// other version.
func (bug *Bug) Merge(repo repository.Repo, other *Bug) (bool, error) {
	// Note: a faster merge should be possible without actually reading and parsing
	// all operations pack of our side.
	// Reading the other side is still necessary to validate remote data, at least
	// for new operations

	if bug.id != other.id {
		return false, errors.New("merging unrelated bugs is not supported")
	}

	if len(other.staging.Operations) > 0 {
		return false, errors.New("merging a bug with a non-empty staging is not supported")
	}

	if bug.lastCommit == "" || other.lastCommit == "" {
		return false, errors.New("can't merge a bug that has never been stored")
	}

	ancestor, err := repo.FindCommonAncestor(bug.lastCommit, other.lastCommit)

	if err != nil {
		return false, err
	}

	ancestorIndex := 0
	newPacks := make([]OperationPack, 0, len(bug.packs))

	// Find the root of the rebase
	for i, pack := range bug.packs {
		newPacks = append(newPacks, pack)

		if pack.commitHash == ancestor {
			ancestorIndex = i
			break
		}
	}

	if len(other.packs) == ancestorIndex+1 {
		// Nothing to rebase, return early
		return false, nil
	}

	// get other bug's extra packs
	for i := ancestorIndex + 1; i < len(other.packs); i++ {
		// clone is probably not necessary
		newPack := other.packs[i].Clone()

		newPacks = append(newPacks, newPack)
		bug.lastCommit = newPack.commitHash
	}

	// rebase our extra packs
	for i := ancestorIndex + 1; i < len(bug.packs); i++ {
		pack := bug.packs[i]

		// get the referenced git tree
		treeHash, err := repo.GetTreeHash(pack.commitHash)

		if err != nil {
			return false, err
		}

		// create a new commit with the correct ancestor
		hash, err := repo.StoreCommitWithParent(treeHash, bug.lastCommit)

		if err != nil {
			return false, err
		}

		// replace the pack
		newPack := pack.Clone()
		newPack.commitHash = hash
		newPacks = append(newPacks, newPack)

		// update the bug
		bug.lastCommit = hash
	}

	// Update the git ref
	err = repo.UpdateRef(bugsRefPattern+bug.id, bug.lastCommit)
	if err != nil {
		return false, err
	}

	return true, nil
}

// Id return the Bug identifier
func (bug *Bug) Id() string {
	if bug.id == "" {
		// simply panic as it would be a coding error
		// (using an id of a bug not stored yet)
		panic("no id yet")
	}
	return bug.id
}

// HumanId return the Bug identifier truncated for human consumption
func (bug *Bug) HumanId() string {
	return formatHumanId(bug.Id())
}

func formatHumanId(id string) string {
	format := fmt.Sprintf("%%.%ds", humanIdLength)
	return fmt.Sprintf(format, id)
}

// Lookup for the very first operation of the bug.
// For a valid Bug, this operation should be a CreateOp
func (bug *Bug) FirstOp() Operation {
	for _, pack := range bug.packs {
		for _, op := range pack.Operations {
			return op
		}
	}

	if !bug.staging.IsEmpty() {
		return bug.staging.Operations[0]
	}

	return nil
}

// Lookup for the very last operation of the bug.
// For a valid Bug, should never be nil
func (bug *Bug) LastOp() Operation {
	if !bug.staging.IsEmpty() {
		return bug.staging.Operations[len(bug.staging.Operations)-1]
	}

	if len(bug.packs) == 0 {
		return nil
	}

	lastPack := bug.packs[len(bug.packs)-1]

	if len(lastPack.Operations) == 0 {
		return nil
	}

	return lastPack.Operations[len(lastPack.Operations)-1]
}

// Compile a bug in a easily usable snapshot
func (bug *Bug) Compile() Snapshot {
	snap := Snapshot{
		id:     bug.id,
		Status: OpenStatus,
	}

	it := NewOperationIterator(bug)

	for it.Next() {
		op := it.Value()
		snap = op.Apply(snap)
		snap.Operations = append(snap.Operations, op)
	}

	return snap
}
