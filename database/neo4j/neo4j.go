package neo4j

import (
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	bolt "github.com/johnnadratowski/golang-neo4j-bolt-driver"
	"github.com/mattes/migrate/database"
)

func init() {
	database.Register("neo4j", &Neo4j{})
}

var DefaultMigrationsLabel = "SchemaMigration"

var (
	ErrNilConfig = fmt.Errorf("no config")
)

type Config struct {
	MigrationsLabel string
	UseTransactions bool
}

type Neo4j struct {
	db       bolt.Conn
	tx       bolt.Tx
	isLocked bool
	config   *Config
}

func WithInstance(instance bolt.Conn, config *Config) (database.Driver, error) {
	if instance == nil || config == nil {
		return nil, ErrNilConfig
	}

	if len(config.MigrationsLabel) == 0 {
		config.MigrationsLabel = DefaultMigrationsLabel
	}

	mx := &Neo4j{
		db:     instance,
		config: config,
	}

	return mx, nil
}

func (m *Neo4j) Open(url string) (database.Driver, error) {
	boltDriver := bolt.NewDriver()
	conn, err := boltDriver.OpenNeo(url)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	driver, err := WithInstance(conn, &Config{})
	if err != nil {
		return nil, err
	}
	return driver, nil
}

func (m *Neo4j) Close() error {
	return m.db.Close()
}

func (m *Neo4j) Lock() error {
	if m.isLocked {
		return database.ErrLocked
	}
	if m.config.UseTransactions {
		tx, err := m.db.Begin()
		if err != nil {
			return &database.Error{OrigErr: err, Err: "transaction start failed"}
		}
		m.tx = tx
	}
	m.isLocked = true
	return nil
}

func (m *Neo4j) Unlock() (err error) {
	m.isLocked = false
	if m.tx != nil {
		if e := m.tx.Commit(); e != nil {
			err = &database.Error{OrigErr: err, Err: "transaction commit failed"}
		}
		m.tx = nil
	}
	return
}

func (m *Neo4j) Rollback() (err error) {
	if m.tx != nil {
		if e := m.tx.Rollback(); e != nil {
			err = &database.Error{OrigErr: err, Err: "transaction rollback failed"}
		}
		m.tx = nil
	}
	return
}

func (m *Neo4j) Run(migration io.Reader) error {
	migr, err := ioutil.ReadAll(migration)
	if err != nil {
		return err
	}

	contents := string(migr[:])
	queries := strings.Split(contents, ";\n")

	for _, query := range queries {

		if len(strings.TrimSpace(query)) == 0 {
			continue
		}

		stmt, err := m.db.PrepareNeo(query)
		if err != nil {
			m.Rollback()
			return &database.Error{OrigErr: err, Query: []byte(query)}
		}
		defer stmt.Close()

		if _, err := stmt.ExecNeo(nil); err != nil {
			m.Rollback()
			return &database.Error{OrigErr: err, Err: "migration failed", Query: []byte(query)}
		}
		// have to close statements in loop
		stmt.Close()
	}

	return nil
}

func (m *Neo4j) SetVersion(version int, dirty bool) error {

	if err := m.Drop(); err != nil {
		m.Rollback()
		return &database.Error{OrigErr: err, Err: "Could not delete migration nodes"}
	}

	if version >= 0 {
		return m.createVersion(version, dirty)
	}

	return nil
}

func (m *Neo4j) createVersion(version int, dirty bool) error {

	query := "CREATE (:" + m.config.MigrationsLabel + " {version:{version}, dirty:{dirty}})"
	stmt, err := m.db.PrepareNeo(query)
	if err != nil {
		m.Rollback()
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}
	defer stmt.Close()
	if _, err := stmt.ExecNeo(map[string]interface{}{"version": version, "dirty": dirty}); err != nil {
		m.Rollback()
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}

	return nil
}

func (m *Neo4j) Version() (version int, dirty bool, err error) {
	query := "MATCH (m:" + m.config.MigrationsLabel + ") return m.version, m.dirty ORDER BY m.version DESC LIMIT 1"
	stmt, err := m.db.PrepareNeo(query)
	if err != nil {
		return 0, false, &database.Error{OrigErr: err, Query: []byte(query)}
	}
	defer stmt.Close()
	rows, err := stmt.QueryNeo(nil)
	data, _, err := rows.NextNeo()
	if err != nil {
		if err == io.EOF {
			return database.NilVersion, false, nil
		}
		return 0, false, &database.Error{OrigErr: err, Query: []byte(query)}
	}

	return int(data[0].(int64)), data[1].(bool), nil
}

func (m *Neo4j) Drop() error {
	// delete all migration nodes
	query := "MATCH (m:" + m.config.MigrationsLabel + ") delete m"
	stmt, err := m.db.PrepareNeo(query)
	if err != nil {
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}
	defer stmt.Close()
	_, err = stmt.ExecNeo(nil)
	if err != nil {
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}

	return nil
}
