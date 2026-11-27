package tunables

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"unsafe"
)

// murmur32 is MurmurHash3 (x86, 32 bits): the same hash for the same bytes in every language, which is what a share of
// traffic is cut by, so that the same user falls on the same side in every service that asks.
func murmur32[B ~string | ~[]byte](data B, seed uint32) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	h := seed
	n := len(data)
	for i := 0; i+4 <= n; i += 4 {
		k := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k *= c1
		k = k<<15 | k>>17
		k *= c2
		h ^= k
		h = h<<13 | h>>19
		h = h*5 + 0xe6546b64
	}
	var k uint32
	tail := n &^ 3
	switch n & 3 {
	case 3:
		k ^= uint32(data[tail+2]) << 16
		fallthrough
	case 2:
		k ^= uint32(data[tail+1]) << 8
		fallthrough
	case 1:
		k ^= uint32(data[tail])
		k *= c1
		k = k<<15 | k>>17
		k *= c2
		h ^= k
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

func hashUint(n uint64, seed uint32) uint32 {
	b := [8]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24), byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56)}
	return murmur32(b[:], seed)
}

// seedOf is what a group cuts its share by: the same user is in the share of one group and out of another's, so the first
// that every feature takes is not always the same people.
func seedOf(feature, group string) uint32 {
	return murmur32(group, murmur32(feature, 0))
}

// keyHash hashes the key of an info (the field tagged `target:"key"`); false when the info has none for this request.
type keyHash func(info unsafe.Pointer, seed uint32) (uint32, bool)

// keyHashOf reads the key field at the offset: a text or a whole number.
func keyHashOf(t reflect.Type, off uintptr) keyHash {
	switch t.Kind() {
	case reflect.String:
		return func(ip unsafe.Pointer, seed uint32) (uint32, bool) {
			s := *(*string)(unsafe.Add(ip, off))
			return murmur32(s, seed), s != ""
		}
	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64:
		return func(ip unsafe.Pointer, seed uint32) (uint32, bool) {
			n := *(*uint64)(unsafe.Add(ip, off))
			return hashUint(n, seed), n != 0
		}
	case reflect.Int32, reflect.Uint32:
		return func(ip unsafe.Pointer, seed uint32) (uint32, bool) {
			n := *(*uint32)(unsafe.Add(ip, off))
			return hashUint(uint64(n), seed), n != 0
		}
	}
	return nil
}

func validKeyType(t reflect.Type) bool { return keyHashOf(t, 0) != nil }

// keyHashOfValue is the same for the key read by reflection (an info that cannot be read by offsets).
func keyHashOfValue(v reflect.Value, seed uint32) (uint32, bool) {
	switch v.Kind() {
	case reflect.String:
		return murmur32(v.String(), seed), v.String() != ""
	case reflect.Int, reflect.Int64, reflect.Int32:
		n := v.Int()
		if v.Kind() == reflect.Int32 {
			return hashUint(uint64(uint32(n)), seed), n != 0
		}
		return hashUint(uint64(n), seed), n != 0
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		return hashUint(v.Uint(), seed), v.Uint() != 0
	}
	return 0, false
}

// inShare reports whether a request is in the share of the traffic the group is for: all of it unless the group has a
// Percent. The share is cut by the hash of the info's key, so that a user always falls on the same side, and a share that
// grows only takes people in; with no key (or a group that is Random) it is the luck of each check.
func (g *Group[T]) inShare(feature string, key func(seed uint32) (uint32, bool)) bool {
	if g.Percent == nil || *g.Percent >= 100 {
		return true
	}
	if *g.Percent <= 0 {
		return false
	}
	if !g.Random && key != nil {
		if h, ok := key(seedOf(feature, g.ID)); ok {
			return int(h%100) < *g.Percent
		}
	}
	return rand.IntN(100) < *g.Percent
}

// infoKeysOf are the fields of an info that are tagged `target:"key"`: the one that says who the request is for.
func infoKeysOf(t reflect.Type) []infoField {
	var out []infoField
	var walk func(t reflect.Type, path []int)
	walk = func(t reflect.Type, path []int) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			idx := append(append([]int{}, path...), i)
			if sf.Anonymous {
				et := sf.Type
				for et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				if et.Kind() == reflect.Struct {
					walk(et, idx)
					continue
				}
			}
			if sf.IsExported() && sf.Tag.Get("target") == "key" {
				out = append(out, infoField{field: sf, index: idx})
			}
		}
	}
	walk(t, nil)
	return out
}

// compileKey makes the hash of the key of the info type (nil when it has none). ok is false when the key is behind an
// embedded pointer, which an offset cannot follow.
func compileKey(it reflect.Type) (key keyHash, ok bool, err error) {
	keys := infoKeysOf(it)
	if len(keys) == 0 {
		return nil, true, nil
	}
	if len(keys) > 1 || !validKeyType(keys[0].field.Type) {
		return nil, false, fmt.Errorf("the key must be one field, a text or a whole number")
	}
	off, err := offsetOf(it, keys[0].index)
	if err != nil {
		return nil, false, nil
	}
	return keyHashOf(keys[0].field.Type, off), true, nil
}

// infoKeyValue is the key of an info that is read by reflection.
func infoKeyValue(info any) (reflect.Value, bool) {
	iv := reflect.ValueOf(info)
	for iv.IsValid() && (iv.Kind() == reflect.Pointer || iv.Kind() == reflect.Interface) {
		if iv.IsNil() {
			return reflect.Value{}, false
		}
		iv = iv.Elem()
	}
	if !iv.IsValid() || iv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	keys := infoKeysOf(iv.Type())
	if len(keys) != 1 {
		return reflect.Value{}, false
	}
	v, err := iv.FieldByIndexErr(keys[0].index)
	return v, err == nil
}
