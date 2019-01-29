package mysqlbatch

import (
	"database/sql"
	"reflect"
	"strconv"
	"time"

	"github.com/moiot/gravity/pkg/config"

	"github.com/json-iterator/go"
	"github.com/moiot/gravity/pkg/position_store"

	"github.com/go-sql-driver/mysql"
	"github.com/juju/errors"
	log "github.com/sirupsen/logrus"

	"github.com/moiot/gravity/pkg/utils"
)

const (
	Unknown       = "*"
	PlainString   = "string"
	PlainInt      = "int"
	PlainBytes    = "bytes"
	SQLNullInt64  = "sqlNullInt64"
	SQLNullString = "sqlNullString"
	SQLNullBool   = "sqlNullBool"
	SQLNullTime   = "sqlNullTime"
	SQLRawBytes   = "sqlRawBytes"
)

var myJson = jsoniter.Config{SortMapKeys: true}.Froze()

func isPositionEquals(p1 *utils.MySQLBinlogPosition, p2 *utils.MySQLBinlogPosition) bool {
	return p1.BinlogGTID == p2.BinlogGTID
}

type TablePosition struct {
	Value  interface{} `toml:"value" json:"value,omitempty"`
	Type   string      `toml:"type" json:"type"`
	Column string      `toml:"column" json:"column"`
}

func (p TablePosition) MapString() (map[string]string, error) {
	pMapString := make(map[string]string)
	pMapString["column"] = p.Column

	switch v := p.Value.(type) {
	case string:
		pMapString["value"] = v
		pMapString["type"] = PlainString
	case int:
		pMapString["value"] = strconv.FormatInt(int64(v), 10)
		pMapString["type"] = PlainInt
	case []byte:
		if v != nil {
			pMapString["value"] = string(v[:])
			pMapString["type"] = PlainBytes
		}
	case sql.RawBytes:
		if v != nil {
			pMapString["value"] = string(v[:])
			pMapString["type"] = SQLRawBytes
		}
	case sql.NullInt64:
		if v.Valid {
			pMapString["value"] = strconv.FormatInt(v.Int64, 10)
			pMapString["type"] = SQLNullInt64
		}
	case sql.NullString:
		if v.Valid {
			pMapString["value"] = v.String
			pMapString["type"] = SQLNullString
		}
	case sql.NullBool:
		if v.Valid {
			pMapString["value"] = strconv.FormatBool(v.Bool)
			pMapString["type"] = SQLNullBool
		}
	case sql.NullFloat64:
		log.Fatalf("not supported")

	case mysql.NullTime:
		if v.Valid {
			pMapString["value"] = v.Time.String()
			pMapString["type"] = SQLNullTime
		}
	default:
		return nil, errors.Errorf("[MapString] unknown type: %v, column: %v", reflect.TypeOf(v).String(), p.Column)
	}
	return pMapString, nil
}

func (p TablePosition) MarshalJSON() ([]byte, error) {
	m, err := p.MapString()
	if err != nil {
		return nil, errors.Trace(err)
	}

	b, err := myJson.Marshal(m)
	if err != nil {
		return nil, errors.Annotatef(err, "[MarshalJSON] failed to marshal column: %v, type: %v, value: %v", p.Column, p.Type, p.Value)
	}
	return b, nil
}

func (p *TablePosition) UnmarshalJSON(value []byte) error {
	pMapString := make(map[string]string)

	if err := myJson.Unmarshal(value, &pMapString); err != nil {
		return errors.Trace(err)
	}

	p.Type = pMapString["type"]
	p.Column = pMapString["column"]
	switch p.Type {
	case PlainString:
		p.Value = pMapString["value"]
	case PlainInt:
		v, err := strconv.Atoi(pMapString["value"])
		if err != nil {
			return errors.Trace(err)
		}
		p.Value = v
	case PlainBytes:
		p.Value = []byte(pMapString["value"])
	case SQLRawBytes:
		// s := []byte(pMapString["value"])
		p.Value = pMapString["value"]
	case SQLNullInt64:
		v, err := strconv.Atoi(pMapString["value"])
		if err != nil {
			return errors.Trace(err)
		}
		p.Value = v
	case SQLNullString:
		p.Value = pMapString["value"]
	case SQLNullBool:
		b, err := strconv.ParseBool(pMapString["value"])
		if err != nil {
			return errors.Trace(err)
		}
		p.Value = b
	case SQLNullTime:
		t, err := time.Parse(time.RFC3339, pMapString["value"])
		if err != nil {
			return errors.Trace(err)
		}
		p.Value = t
	default:
		return errors.Errorf("[UnmarshalJSON] unknown type: %v, column: %v", p.Type, p.Column)
	}
	return nil
}

type BatchPositionValue struct {
	Start   *utils.MySQLBinlogPosition `toml:"start-binlog" json:"start-binlog"`
	Min     map[string]TablePosition   `toml:"min" json:"min"`
	Max     map[string]TablePosition   `toml:"max" json:"max"`
	Current map[string]TablePosition   `toml:"current" json:"current"`
}

func Serialize(positions *BatchPositionValue) (string, error) {
	s, err := myJson.MarshalToString(positions)
	if err != nil {
		return "", errors.Trace(err)
	}
	return s, nil
}

func Deserialize(value string) (*BatchPositionValue, error) {
	positions := BatchPositionValue{}
	if err := myJson.UnmarshalFromString(value, &positions); err != nil {
		return nil, errors.Trace(err)
	}
	return &positions, nil
}

func InitPositionCache(cache position_store.PositionCacheInterface, sourceDB *sql.DB) error {
	position, exist, err := cache.Get()
	if err != nil {
		return errors.Trace(err)
	}

	if !exist {
		dbUtil := utils.NewMySQLDB(sourceDB)
		binlogFilePos, gtid, err := dbUtil.GetMasterStatus()
		if err != nil {
			return errors.Trace(err)
		}

		startPosition := utils.MySQLBinlogPosition{
			BinLogFileName: binlogFilePos.Name,
			BinLogFilePos:  binlogFilePos.Pos,
			BinlogGTID:     gtid.String(),
		}

		batchPositions := BatchPositionValue{
			Start: &startPosition,
		}
		v, err := Serialize(&batchPositions)
		if err != nil {
			return errors.Trace(err)
		}

		position.Value = v
		position.Stage = config.Batch
		position.UpdateTime = time.Now()
		if err := cache.Put(position); err != nil {
			return errors.Trace(err)
		}
		if err := cache.Flush(); err != nil {
			return errors.Trace(err)
		}
	}

	return nil
}

func GetStartBinlog(cache position_store.PositionCacheInterface) (*utils.MySQLBinlogPosition, error) {
	position, exist, err := cache.Get()
	if err != nil {
		return nil, errors.Trace(err)
	}

	if !exist {
		return nil, errors.Errorf("empty position")
	}

	batchPositions, err := Deserialize(position.Value)
	if err != nil {
		return nil, errors.Trace(err)
	}

	return batchPositions.Start, nil
}

func GetCurrentPos(cache position_store.PositionCacheInterface, fullTableName string) (*TablePosition, bool, error) {
	position, exist, err := cache.Get()
	if err != nil {
		return nil, false, errors.Trace(err)
	}

	if !exist {
		return nil, false, nil
	}

	batchPositions, err := Deserialize(position.Value)
	if err != nil {
		return nil, false, errors.Trace(err)
	}

	current, ok := batchPositions.Current[fullTableName]
	if !ok {
		return nil, false, nil
	}
	return &current, true, nil
}

func PutCurrentPos(cache position_store.PositionCacheInterface, fullTableName string, pos *TablePosition) error {
	position, exist, err := cache.Get()
	if err != nil {
		return errors.Trace(err)
	}

	if !exist {
		return errors.Errorf("empty position")
	}

	batchPositions, err := Deserialize(position.Value)
	if err != nil {
		return errors.Trace(err)
	}

	batchPositions.Current[fullTableName] = *pos
	v, err := Serialize(batchPositions)
	if err != nil {
		return errors.Trace(err)
	}
	position.Value = v
	return errors.Trace(cache.Put(position))
}

func GetMaxMin(cache position_store.PositionCacheInterface, fullTableName string) (*TablePosition, *TablePosition, bool, error) {
	position, exist, err := cache.Get()
	if err != nil {
		return nil, nil, false, errors.Trace(err)
	}

	if !exist {
		return nil, nil, false, nil
	}

	batchPositions, err := Deserialize(position.Value)
	if err != nil {
		return nil, nil, false, errors.Trace(err)
	}

	max, ok := batchPositions.Max[fullTableName]
	if !ok {
		return nil, nil, false, nil
	}

	min, ok := batchPositions.Min[fullTableName]
	if !ok {
		return nil, nil, false, nil
	}

	return &max, &min, true, nil
}

func PutMaxMin(cache position_store.PositionCacheInterface, fullTableName string, max *TablePosition, min *TablePosition) error {
	position, exist, err := cache.Get()
	if err != nil {
		return errors.Trace(err)
	}

	if !exist {
		return errors.Errorf("empty position")
	}

	batchPositions, err := Deserialize(position.Value)
	if err != nil {
		return errors.Trace(err)
	}

	batchPositions.Max[fullTableName] = *max
	batchPositions.Min[fullTableName] = *min

	return errors.Trace(cache.Put(position))
}
