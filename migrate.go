package tmsqlite

import (
	"context"
	"database/sql"
	"fmt"
)

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS migrations(
  id INTEGER PRIMARY KEY CHECK (id = 0),
  version INTEGER
);`,
	); err != nil {
		return fmt.Errorf("error getting initial migrations table: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO migrations(id, version) VALUES (0, 0)`,
	); err != nil {
		return fmt.Errorf("error setting initial migration version: %w", err)
	}

	row := tx.QueryRowContext(ctx, `SELECT version FROM migrations WHERE id=0;`)
	if err != nil {
		return fmt.Errorf("failed to select from migrations table: %w", err)
	}

	var migrationVersion int
	if err := row.Scan(&migrationVersion); err != nil {
		return fmt.Errorf("failed to scan migration version: %w", err)
	}

	if err := migrateFrom(ctx, tx, migrationVersion); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	return nil
}

func migrateFrom(ctx context.Context, tx *sql.Tx, version int) error {
	switch version {
	case 0:
		if err := migrateInitial(ctx, tx); err != nil {
			return fmt.Errorf("initial migration: %w", err)
		}
		if err := setMigrationVersion(ctx, tx, 1); err != nil {
			return err
		}
	case 1:
		// Up to date.
		return nil
	default:
		return fmt.Errorf("unknown migration version %d", version)
	}

	// If we didn't return inside the above switch statement,
	// then we did something with migrations.
	// According to https://sqlite.org/pragma.html#pragma_optimize,
	// "All applications should run `PRAGMA optimize;` after a schema change,
	// especially after one or more CREATE INDEX statements."
	// Creating tables is a schema change, so here we go.
	if _, err := tx.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return fmt.Errorf("failed to run PRAGMA optimize after migration: %w", err)
	}

	return nil
}

func migrateInitial(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(
		ctx,
		// The mirror table tracks the mirror's perceived voting and committing heights and rounds.
		//
		// CHECK id=0 is a pattern for limiting table to a single row:
		// https://stackoverflow.com/a/33104119
		// We use that to ensure all updates are manual and affect the single row.
		`
CREATE TABLE mirror(
  id INTEGER PRIMARY KEY CHECK (id = 0),
  vh INTEGER NOT NULL, vr INTEGER NOT NULL,
  ch INTEGER NOT NULL, cr INTEGER NOT NULL
);
INSERT INTO mirror VALUES(0, 0, 0, 0, 0);`+

			// Much like the mirror table, the state machine also has to track its height and round.
			`
CREATE TABLE state_machine(
  id INTEGER PRIMARY KEY CHECK (id = 0),
  h INTEGER NOT NULL, r INTEGER NOT NULL
);
INSERT INTO state_machine VALUES(0, 0, 0);`+

			// Unique validator public keys.
			// The type column is the result of the [gcrypto.PubKey.TypeName] method.
			// The key column is the result of the [gcrypto.PubKey.PubKeyBytes] method.
			// The type and key together can be passed to [gcrypto.Registry.Decode]
			// to reconstitute a [gcrypto.PubKey].
			// The length check of 8 matches the internals of gcrypto.
			`
CREATE TABLE validator_pub_keys(
  id INTEGER PRIMARY KEY NOT NULL,
  type TEXT NOT NULL CHECK(octet_length(type) > 0 AND octet_length(type) <= 8),
  key BLOB NOT NULL UNIQUE
);`+

			// Hashes of collections of validator public keys.
			`
CREATE TABLE validator_pub_key_hashes(
  id INTEGER PRIMARY KEY NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  n_keys INTEGER NOT NULL CHECK (n_keys > 0)
);`+

			// Many-to-many relationship of the validator_pub_key_hashes table
			// and individual keys.
			`
CREATE TABLE validator_pub_key_hash_entries(
  id INTEGER PRIMARY KEY NOT NULL,
  hash_id INTEGER NOT NULL,
  idx INTEGER NOT NULL,
  key_id INTEGER NOT NULL,
  FOREIGN KEY(key_id) REFERENCES validator_pub_keys(id),
  FOREIGN KEY(hash_id) REFERENCES validator_pub_key_hashes(id),
  UNIQUE (hash_id, idx),
  UNIQUE (hash_id, key_id)
);`+

			// View to simplify querying public keys by hash.
			`
CREATE VIEW validator_pub_keys_for_hash(
  hash_id, hash, n_keys,
  idx, type, key
) AS SELECT
  validator_pub_key_hashes.id, validator_pub_key_hashes.hash, validator_pub_key_hashes.n_keys,
  idx, type, key FROM validator_pub_keys
JOIN validator_pub_key_hash_entries
  ON validator_pub_keys.id = validator_pub_key_hash_entries.key_id
JOIN validator_pub_key_hashes
  ON validator_pub_key_hash_entries.hash_id = validator_pub_key_hashes.id;`+

			// Hashes of collections of validator powers.
			`
CREATE TABLE validator_power_hashes(
  id INTEGER PRIMARY KEY NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  n_powers INTEGER NOT NULL CHECK (n_powers > 0)
);`+

			// Many-to-many relationship between validator_power_hashes and power values.
			`
CREATE TABLE validator_power_hash_entries(
  id INTEGER PRIMARY KEY NOT NULL,
  hash_id INTEGER NOT NULL,
  idx INTEGER NOT NULL,
  power INTEGER NOT NULL,
  FOREIGN KEY(hash_id) REFERENCES validator_power_hashes(id),
  UNIQUE (hash_id, idx)
);`+

			// View to simplify querying powers by hash.
			`
CREATE VIEW validator_powers_for_hash(
  hash_id, hash, n_powers,
  idx, power
) AS SELECT
  validator_power_hashes.id, validator_power_hashes.hash, validator_power_hashes.n_powers,
  idx, power FROM validator_power_hash_entries
JOIN validator_power_hashes
  ON validator_power_hashes.id = validator_power_hash_entries.hash_id;`+

			// Finalizations as reported by the state machine.
			`
CREATE TABLE finalizations(
  height INTEGER PRIMARY KEY NOT NULL,
  round INTEGER NOT NULL,
  block_hash BLOB NOT NULL,
  validator_pub_key_hash_id INTEGER NOT NULL,
  validator_power_hash_id INTEGER NOT NULL,
  app_state_hash BLOB NOT NULL,
  FOREIGN KEY(validator_pub_key_hash_id) REFERENCES validator_pub_key_hashes(id),
  FOREIGN KEY(validator_power_hash_id) REFERENCES validator_power_hashes(id)
);`+

			// A somewhat generic header table.
			// We allow both prev_block_hash and prev_commit_proof_id
			// to be null for the initial header.
			`
CREATE TABLE headers(
  id INTEGER PRIMARY KEY NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  prev_block_hash BLOB,
  height INTEGER NOT NULL CHECK (height >= 0),
  prev_commit_proof_id INTEGER,
  validators_pub_key_hash_id INTEGER NOT NULL,
  validators_power_hash_id INTEGER NOT NULL,
  next_validators_pub_key_hash_id INTEGER NOT NULL,
  next_validators_power_hash_id INTEGER NOT NULL,
  data_id BLOB NOT NULL,
  prev_app_state_hash BLOB NOT NULL,
  user_annotations BLOB,
  driver_annotations BLOB,
  committed INTEGER NOT NULL CHECK (committed = 0 OR committed = 1),
  FOREIGN KEY(prev_commit_proof_id) REFERENCES commit_proofs(id),
  FOREIGN KEY(validators_pub_key_hash_id) REFERENCES validator_pub_key_hashes(id),
  FOREIGN KEY(validators_power_hash_id) REFERENCES validator_power_hashes(id),
  FOREIGN KEY(next_validators_pub_key_hash_id) REFERENCES validator_pub_key_hashes(id),
  FOREIGN KEY(next_validators_power_hash_id) REFERENCES validator_power_hashes(id)
);`+

			// Table for any commit proofs,
			// whether it is a header's (nullable) previous commit proof,
			// or the subjective commit proof for a header.
			`
CREATE TABLE commit_proofs(
  id INTEGER PRIMARY KEY NOT NULL,
  round INTEGER NOT NULL CHECK (round >= 0),
  validators_pub_key_hash_id INTEGER NOT NULL,
  FOREIGN KEY(validators_pub_key_hash_id) REFERENCES validator_pub_key_hashes(id)
);`+

			// Which block hashes are present in a commit proof.
			// Null block hash indicates a vote for nil.
			`
CREATE TABLE commit_proof_voted_blocks(
  id INTEGER PRIMARY KEY NOT NULL,
  commit_proof_id INTEGER NOT NULL,
  block_hash BLOB,
  FOREIGN KEY(commit_proof_id) REFERENCES commit_proofs(id),
  UNIQUE (commit_proof_id, block_hash)
);`+

			// Many-to-many relationship indicating
			// which signatures are present for a particular block in a previous commit proof.
			`
CREATE TABLE commit_proof_block_signatures(
  id INTEGER PRIMARY KEY NOT NULL,
  commit_proof_voted_block_id INTEGER NOT NULL,
  sparse_signature_id INTEGER NOT NULL,
  FOREIGN KEY(commit_proof_voted_block_id) REFERENCES commit_proof_voted_blocks(id),
  FOREIGN KEY(sparse_signature_id) REFERENCES sparse_signatures(id) ON DELETE CASCADE,
  UNIQUE (commit_proof_voted_block_id, sparse_signature_id)
);`+

			// Pairings of key IDs (whose format is dependent on the signature scheme)
			// and raw signature bytes.
			`
CREATE TABLE sparse_signatures(
  id INTEGER PRIMARY KEY NOT NULL,
  key_id BLOB NOT NULL,
  signature BLOB NOT NULL
);`+

			// Simplified view to input only a commit_proof_id
			// and get back all the sparse signatures.
			//
			// Note, the structure of the proof signatures
			// is a map with block hash keys,
			// and values which are slices of sparse signatures.
			// It is important that the elements in the slices
			// are loaded from the database in the same order they were inserted.
			// We assume they are inserted in the application-specific order,
			// so ordering by the
			`
CREATE VIEW v_proof_signatures(
  commit_proof_id,
  block_hash,
  key_id,
  signature,
  sig_rowid
) AS SELECT
  blocks.commit_proof_id,
  blocks.block_hash,
  sigs.key_id,
  sigs.signature,
  sigs.rowid
FROM commit_proof_block_signatures
JOIN
  sparse_signatures AS sigs
  ON sigs.id = commit_proof_block_signatures.sparse_signature_id
JOIN
  commit_proof_voted_blocks AS blocks
  ON blocks.id = commit_proof_block_signatures.commit_proof_voted_block_id
;`+

			// The committed headers table contains an ID of a header
			// and the ID of a subjective proof that the header is committing.
			`
CREATE TABLE committed_headers(
  id INTEGER PRIMARY KEY NOT NULL,
  header_id INTEGER NOT NULL,
  proof_id INTEGER NOT NULL,
  FOREIGN KEY(header_id) REFERENCES headers(id),
  FOREIGN KEY(proof_id) REFERENCES commit_proofs(id)
);`+

			// A somewhat generic proposed header table.
			// A proposed header is just a reference to a header,
			// plus some extra metadata.
			`
CREATE TABLE proposed_headers(
  id INTEGER PRIMARY KEY NOT NULL,
  header_id INTEGER NOT NULL,
  height INTEGER NOT NULL CHECK (height >= 0),
  round INTEGER NOT NULL CHECK (round >= 0),
  proposer_pub_key_id INTEGER NOT NULL,
  user_annotations BLOB,
  driver_annotations BLOB,
  signature BLOB NOT NULL,
  FOREIGN KEY(header_id) REFERENCES headers(id),
  FOREIGN KEY(proposer_pub_key_id) REFERENCES validator_pub_keys(id),
  UNIQUE (height, round, proposer_pub_key_id)
);`+

			// The headers that the local state machine has proposed to the network.
			// Although the height and round can be determined through the proposed header,
			// it is much simpler to just expose the height and round on this table.
			///
			// We check that the height is greater than 0;
			// while the header store accepts headers for height 0,
			// that is an edge case for the initial block.
			// Nobody will propose a block with height 0.
			`
CREATE TABLE actions_proposed_headers(
  id INTEGER PRIMARY KEY NOT NULL,
  height INTEGER NOT NULL CHECK (height > 0),
  round INTEGER NOT NULL CHECK (round >= 0),
  proposed_header_id INTEGER NOT NULL,
  FOREIGN KEY(proposed_header_id) REFERENCES proposed_headers(id),
  UNIQUE (height, round)
);`+

			// The prevotes that the local state machine has sent.
			`
CREATE TABLE actions_prevotes(
  id INTEGER PRIMARY KEY NOT NULL,
  height INTEGER NOT NULL CHECK (height > 0),
  round INTEGER NOT NULL CHECK (round >= 0),
  signer_pub_key_id INTEGER NOT NULL,
  block_hash BLOB,
  signature BLOB NOT NULL,
  FOREIGN KEY(signer_pub_key_id) REFERENCES validator_pub_keys(id),
  UNIQUE (height, round)
);`+

			// The precommits that the local state machine has sent.
			// (Identical to the actions_prevotes table.)
			`
CREATE TABLE actions_precommits(
  id INTEGER PRIMARY KEY NOT NULL,
  height INTEGER NOT NULL CHECK (height > 0),
  round INTEGER NOT NULL CHECK (round >= 0),
  signer_pub_key_id INTEGER NOT NULL,
  block_hash BLOB,
  signature BLOB NOT NULL,
  FOREIGN KEY(signer_pub_key_id) REFERENCES validator_pub_keys(id),
  UNIQUE (height, round)
);`+

			// Mostly joined view of the three individual actions tables.
			// This is not working properly yet when filtering by height,
			// if any table is lacking a record for that height and column.
			`
CREATE VIEW actions(
  height,
  round,
  ph_id,
  pv_block_hash, pv_signature, pv_key_id,
  pc_block_hash, pc_signature, pc_key_id
) AS
SELECT
  COALESCE(phs.height, pvs.height, pcs.height),
  COALESCE(phs.round, pvs.round, pcs.round),
  phs.proposed_header_id,
  pvs.block_hash, pvs.signature, pvs.signer_pub_key_id,
  pcs.block_hash, pcs.signature, pcs.signer_pub_key_id
FROM actions_proposed_headers AS phs
FULL OUTER JOIN actions_prevotes AS pvs ON pvs.height = phs.height AND pvs.round = phs.round
FULL OUTER JOIN actions_precommits AS pcs ON pcs.height = phs.height AND pcs.round = phs.round;
`+

			// The round_votes table tracks the prevotes and precommits
			// that the mirror observes during a single round.
			// This is similar to the commit proofs table,
			// but it has a bit of a semantic difference
			// in collecting live prevotes and precommits,
			// rather than finalized commit proofs.
			// Furthermore, it has a different access pattern in that
			// rows in round_*_signatures may be deleted
			// as new votes are received, if signature aggregation is used.
			`
CREATE TABLE round_votes(
  id INTEGER PRIMARY KEY NOT NULL,
  height INTEGER NOT NULL CHECK (height > 0),
  round INTEGER NOT NULL CHECK (round >= 0),
  validators_pub_key_hash_id INTEGER NOT NULL,
  FOREIGN KEY(validators_pub_key_hash_id) REFERENCES validator_pub_key_hashes(id),
  UNIQUE (height, round)
);`+

			// The blocks in the round which have received prevotes.
			`
CREATE TABLE round_prevote_blocks(
  id INTEGER PRIMARY KEY NOT NULL,
  block_hash BLOB,
  round_vote_id INTEGER NOT NULL,
  FOREIGN KEY(round_vote_id) REFERENCES round_votes(id) ON DELETE CASCADE,
  UNIQUE (round_vote_id, block_hash)
);`+

			// The blocks in the round which have received precommits.
			// (Identical schema to round_prevote_blocks.)
			`
CREATE TABLE round_precommit_blocks(
  id INTEGER PRIMARY KEY NOT NULL,
  block_hash BLOB,
  round_vote_id INTEGER NOT NULL,
  FOREIGN KEY(round_vote_id) REFERENCES round_votes(id) ON DELETE CASCADE,
  UNIQUE (round_vote_id, block_hash)
);`+

			// The prevote signatures for a particular block or null,
			// within a particular round.
			`
CREATE TABLE round_prevote_signatures(
  id INTEGER PRIMARY KEY NOT NULL,
  block_id INTEGER NOT NULL,
  key_id BLOB NOT NULL,
  signature BLOB NOT NULL,
  FOREIGN KEY(block_id) REFERENCES round_prevote_blocks(id) ON DELETE CASCADE,
  UNIQUE (block_id, key_id)
);`+

			// The precommit signatures for a particular block or null,
			// within a particular round.
			`
CREATE TABLE round_precommit_signatures(
  id INTEGER PRIMARY KEY NOT NULL,
  block_id INTEGER NOT NULL,
  key_id BLOB NOT NULL,
  signature BLOB NOT NULL,
  FOREIGN KEY(block_id) REFERENCES round_precommit_blocks(id) ON DELETE CASCADE,
  UNIQUE (block_id, key_id)
);`+

			// Views for getting prevotes and precommits from round_votes.
			`
CREATE VIEW round_prevotes(
  height, round,
  pub_key_hash,
  block_hash,
  key_id, sig
) AS SELECT
  rv.height, rv.round,
  keys.hash,
  blocks.block_hash,
  sigs.key_id, sigs.signature
FROM validator_pub_key_hashes AS keys
JOIN round_votes AS rv ON rv.validators_pub_key_hash_id = keys.id
JOIN round_prevote_blocks AS blocks ON blocks.round_vote_id = rv.id
JOIN round_prevote_signatures AS sigs ON sigs.block_id = blocks.id;

CREATE VIEW round_precommits(
  height, round,
  pub_key_hash,
  block_hash,
  key_id, sig
) AS SELECT
  rv.height, rv.round,
  keys.hash,
  blocks.block_hash,
  sigs.key_id, sigs.signature
FROM validator_pub_key_hashes AS keys
JOIN round_votes AS rv ON rv.validators_pub_key_hash_id = keys.id
JOIN round_precommit_blocks AS blocks ON blocks.round_vote_id = rv.id
JOIN round_precommit_signatures AS sigs ON sigs.block_id = blocks.id;
`+

			// Consistent end of long concatenated literal, to minimize diffs.
			"",
	)

	return err
}

func setMigrationVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE migrations SET version = ? WHERE id = 0`,
		version,
	); err != nil {
		return fmt.Errorf("error setting migration version to %d: %w", version, err)
	}

	return nil
}
