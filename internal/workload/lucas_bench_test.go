package workload

import "testing"

func benchLL(b *testing.B, p uint) {
	s := newLL(p)
	s.set(4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.step()
	}
}

func BenchmarkLL1K(b *testing.B)   { benchLL(b, 4423) }
func BenchmarkLL4K(b *testing.B)   { benchLL(b, 21701) }
func BenchmarkLL256K(b *testing.B) { benchLL(b, 1257787) }
