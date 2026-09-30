package platform

import (
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"uuid"
)

// A struct that carries a RepositoryKey on a JSON wire tags the key field
//
//	Key RepositoryKey `json:"-" repokey:"platform_repo_id,bitbucket_repository_uuid"`
//
// naming the integer and UUID members, with an optional trailing ",omitempty"
// for an integer member that may be absent. Its JSON methods delegate to
// MarshalKeyedJSON and UnmarshalKeyedJSON through a method-less copy of the
// type:
//
//	func (r T) MarshalJSON() ([]byte, error) { type plain T; return MarshalKeyedJSON(plain(r)) }
//	func (r *T) UnmarshalJSON(b []byte) error { type plain T; return UnmarshalKeyedJSON(b, (*plain)(r)) }
//
// The wire form is the same struct with the key field replaced, in place, by
// its two members, so every other field keeps its tags and position.
const repositoryKeyTag = "repokey"

var repositoryKeyType = reflect.TypeFor[RepositoryKey]()

// keyedWire maps a key-bearing struct type to its flat wire struct type.
type keyedWire struct {
	wire reflect.Type
	// source[i] is the index in the key-bearing type of wire field i, -1
	// for the members that encode a key (keyOf[i] names that key field), or
	// -2 for the marker field.
	source []int
	keyOf  []int
}

var keyedWires sync.Map // reflect.Type -> *keyedWire or error

// RepositoryKeyWireType returns the flat wire struct type JSON uses for t, a
// struct with a repokey-tagged RepositoryKey field. API schemas document t
// by this type. ok is false when t carries no tagged key.
func RepositoryKeyWireType(t reflect.Type) (wire reflect.Type, ok bool, err error) {
	w, err := keyedWireFor(t)
	if err != nil || w == nil {
		return nil, false, err
	}
	return w.wire, true, nil
}

func keyedWireFor(t reflect.Type) (*keyedWire, error) {
	if cached, ok := keyedWires.Load(t); ok {
		if err, isErr := cached.(error); isErr {
			return nil, err
		}
		return cached.(*keyedWire), nil
	}
	w, err := buildKeyedWire(t)
	if err != nil {
		keyedWires.Store(t, err)
		return nil, err
	}
	keyedWires.Store(t, w)
	return w, nil
}

func buildKeyedWire(t reflect.Type) (*keyedWire, error) {
	if t.Kind() != reflect.Struct {
		return nil, nil
	}
	var fields []reflect.StructField
	w := &keyedWire{}
	tagged := false
	for i := range t.NumField() {
		field := t.Field(i)
		spec, isKey := field.Tag.Lookup(repositoryKeyTag)
		if isKey && field.Type == repositoryKeyType {
			tagged = true
			idName, uuidName, omitID, err := parseRepositoryKeyTag(spec)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", t, field.Name, err)
			}
			idTag := idName
			if omitID {
				idTag += ",omitempty"
			}
			fields = append(fields,
				reflect.StructField{Name: field.Name + "WireID", Type: reflect.TypeFor[int64](), Tag: reflect.StructTag(`json:"` + idTag + `"`)},
				reflect.StructField{Name: field.Name + "WireUUID", Type: reflect.TypeFor[uuid.UUID](), Tag: reflect.StructTag(`json:"` + uuidName + `,omitzero"`)},
			)
			w.source = append(w.source, -1, -1)
			w.keyOf = append(w.keyOf, i, i)
			continue
		}
		if !field.IsExported() {
			if field.Anonymous {
				return nil, fmt.Errorf("%s: unexported embedded field %s cannot carry a repository key wire form", t, field.Name)
			}
			continue
		}
		fields = append(fields, reflect.StructField{
			Name: field.Name, Type: field.Type, Tag: field.Tag, Anonymous: field.Anonymous,
		})
		w.source = append(w.source, i)
		w.keyOf = append(w.keyOf, -1)
	}
	if !tagged {
		return nil, nil
	}
	// StructOf returns one type for identical layouts; the zero-size marker
	// keeps each source type's wire type, and so its schema name, distinct.
	fields = append(fields, reflect.StructField{
		Name: "RepositoryKeyWireOf", Type: reflect.ArrayOf(0, t), Tag: `json:"-"`,
	})
	w.source = append(w.source, -2)
	w.keyOf = append(w.keyOf, -1)
	w.wire = reflect.StructOf(fields)
	return w, nil
}

func parseRepositoryKeyTag(spec string) (idName, uuidName string, omitID bool, err error) {
	parts := strings.Split(spec, ",")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false, fmt.Errorf("repokey tag %q must name the integer and UUID members", spec)
	}
	for _, option := range parts[2:] {
		if option != "omitempty" {
			return "", "", false, fmt.Errorf("repokey tag %q: unknown option %q", spec, option)
		}
		omitID = true
	}
	return parts[0], parts[1], omitID, nil
}

// MarshalKeyedJSON encodes v, a method-less copy of a key-bearing struct, in
// its flat wire form.
func MarshalKeyedJSON(v any) ([]byte, error) {
	value := reflect.ValueOf(v)
	w, err := keyedWireFor(value.Type())
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("%s has no repokey-tagged RepositoryKey field", value.Type())
	}
	wire := reflect.New(w.wire).Elem()
	for i := 0; i < w.wire.NumField(); i++ {
		if src := w.source[i]; src >= 0 {
			wire.Field(i).Set(value.Field(src))
			continue
		} else if src == -2 {
			continue
		}
		key := value.Field(w.keyOf[i]).Interface().(RepositoryKey)
		id, repositoryUUID := key.Wire()
		wire.Field(i).Set(reflect.ValueOf(id))
		wire.Field(i + 1).Set(reflect.ValueOf(repositoryUUID))
		i++
	}
	return json.Marshal(wire.Interface())
}

// UnmarshalKeyedJSON decodes data, in its flat wire form, into ptr, a pointer
// to a method-less copy of a key-bearing struct.
func UnmarshalKeyedJSON(data []byte, ptr any) error {
	value := reflect.ValueOf(ptr).Elem()
	w, err := keyedWireFor(value.Type())
	if err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("%s has no repokey-tagged RepositoryKey field", value.Type())
	}
	wire := reflect.New(w.wire)
	if err := json.Unmarshal(data, wire.Interface()); err != nil {
		return err
	}
	wire = wire.Elem()
	value.SetZero()
	for i := 0; i < w.wire.NumField(); i++ {
		if src := w.source[i]; src >= 0 {
			value.Field(src).Set(wire.Field(i))
			continue
		} else if src == -2 {
			continue
		}
		key, err := RepositoryKeyFromWire(wire.Field(i).Int(), wire.Field(i+1).Interface().(uuid.UUID))
		if err != nil {
			return err
		}
		value.Field(w.keyOf[i]).Set(reflect.ValueOf(key))
		i++
	}
	return nil
}
