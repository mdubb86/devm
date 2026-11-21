package schema

import (
	"fmt"
	"iter"
	"sort"

	"gopkg.in/yaml.v3"
)

// OrderedMap is a string-keyed map whose keys retain their YAML
// source order. Lifecycle sequencing (service startup, snapshot
// pre/post hooks) depends on this: Go's native map is unordered,
// so the YAML author's intent would be lost on the round-trip.
//
// The zero value is usable: Len() reports 0, Get returns the zero T
// with ok=false, and Iter() yields nothing.
type OrderedMap[T any] struct {
	Entries map[string]T
	Order   []string
}

// Len returns the number of entries in the map.
func (m OrderedMap[T]) Len() int {
	return len(m.Entries)
}

// Get returns the value stored under k and whether it was present.
func (m OrderedMap[T]) Get(k string) (T, bool) {
	v, ok := m.Entries[k]
	return v, ok
}

// Iter yields each entry in declared order.
func (m OrderedMap[T]) Iter() iter.Seq2[string, T] {
	return func(yield func(string, T) bool) {
		for _, k := range m.Order {
			if !yield(k, m.Entries[k]) {
				return
			}
		}
	}
}

// Set stores v under k. New keys are appended to Order; existing
// keys keep their position.
func (m *OrderedMap[T]) Set(k string, v T) {
	if m.Entries == nil {
		m.Entries = map[string]T{}
	}
	if _, ok := m.Entries[k]; !ok {
		m.Order = append(m.Order, k)
	}
	m.Entries[k] = v
}

// Delete removes k from the map and from Order.
func (m *OrderedMap[T]) Delete(k string) {
	if _, ok := m.Entries[k]; !ok {
		return
	}
	delete(m.Entries, k)
	for i, name := range m.Order {
		if name == k {
			m.Order = append(m.Order[:i], m.Order[i+1:]...)
			return
		}
	}
}

// UnmarshalYAML walks the mapping node pair-by-pair so the key
// order survives the decode. Duplicate keys surface as an error
// naming the key.
func (m *OrderedMap[T]) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping, got %v", node.Kind)
	}
	n := len(node.Content) / 2
	m.Entries = make(map[string]T, n)
	m.Order = make([]string, 0, n)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, dup := m.Entries[key]; dup {
			return fmt.Errorf("duplicate key %q at line %d", key, node.Content[i].Line)
		}
		var val T
		if err := node.Content[i+1].Decode(&val); err != nil {
			return fmt.Errorf("%q: %w", key, err)
		}
		m.Entries[key] = val
		m.Order = append(m.Order, key)
	}
	return nil
}

// MarshalYAML emits a mapping node with the entries in declared
// order so a yaml.Marshal → yaml.Unmarshal round-trip preserves it.
func (m OrderedMap[T]) MarshalYAML() (interface{}, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range m.Order {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
		valNode := &yaml.Node{}
		if err := valNode.Encode(m.Entries[k]); err != nil {
			return nil, fmt.Errorf("%q: %w", k, err)
		}
		out.Content = append(out.Content, keyNode, valNode)
	}
	return out, nil
}

// OrderedMapOf wraps a plain map as an OrderedMap, deriving Order
// from a sorted scan of the keys. Intended for programmatic
// construction (tests, synthetic configs) where no source order
// exists; production decode paths come through UnmarshalYAML.
func OrderedMapOf[T any](m map[string]T) OrderedMap[T] {
	if m == nil {
		return OrderedMap[T]{}
	}
	order := make([]string, 0, len(m))
	for k := range m {
		order = append(order, k)
	}
	sort.Strings(order)
	return OrderedMap[T]{Entries: m, Order: order}
}
