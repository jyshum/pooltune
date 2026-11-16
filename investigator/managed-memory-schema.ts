import {z} from 'zod';
const bytes=z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER);
export const managedMemorySchema=z.strictObject({
 function_oid:z.number().int().positive().max(4294967295),baseline_work_mem_bytes:bytes,
 candidate_bytes:bytes,concurrent_boost_slots:z.number().int().min(1).max(3),
 available_memory_bytes:bytes,total_memory_bytes:bytes,memory_observed_at:z.iso.datetime(),
 outer_temp_bytes:z.number().nonnegative(),inner_temp_bytes:z.number().nonnegative(),
 sort_rows:z.number().int().min(1).max(100000),identity_verified:z.boolean(),fallback_preserves_baseline:z.boolean(),
});
export function managedMemoryCause(m:z.infer<typeof managedMemorySchema>,now=Date.now()) {
 const age=now-Date.parse(m.memory_observed_at);
 const reserve=Math.max(128*1024*1024,m.total_memory_bytes/4);
 return age>=0&&age<=30000&&m.baseline_work_mem_bytes>=65536&&m.baseline_work_mem_bytes<m.candidate_bytes&&m.candidate_bytes===33554432&&
  m.available_memory_bytes<=m.total_memory_bytes&&m.available_memory_bytes-reserve>=2*m.candidate_bytes*m.concurrent_boost_slots&&
  m.outer_temp_bytes>=8388608&&m.inner_temp_bytes>=8388608&&m.identity_verified&&m.fallback_preserves_baseline;
}
