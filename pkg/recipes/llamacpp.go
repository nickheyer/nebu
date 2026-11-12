package recipes

import (
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Builds llama-server with the host backend.
type LlamaCpp struct{}

func (LlamaCpp) ID() string        { return "llamacpp" }
func (LlamaCpp) RuntimeID() string { return "llamacpp" }
func (LlamaCpp) Description() string {
	return "llama-server compiled from a tag, branch, or commit of llama.cpp"
}

func (LlamaCpp) Source() Source {
	return Source{
		Releases: "ggml-org/llama.cpp",
		// GitHub serves a tag, a branch, or a commit alike at this path
		Archive: func(ref string) string {
			return "https://github.com/ggml-org/llama.cpp/archive/" + ref + ".tar.gz"
		},
	}
}

// cmake configures and drives the build, cc and c++ compile it
func (LlamaCpp) Tools() []string { return []string{"cmake", "cc", "c++"} }
func (LlamaCpp) Facts() []string {
	return []string{"device.vendor", "device.compute_capability", "device.driver_version"}
}
func (LlamaCpp) Vars() []Var {
	return []Var{
		{Name: "build_type", Label: "Build type", Default: "Release", Description: "The cmake build type", Choices: []string{"Release", "RelWithDebInfo", "Debug", "MinSizeRel"}},
		{Name: "extra", Label: "Extra cmake flags", Description: "Space-separated CMake flags, such as -DGGML_CUDA_F16=ON"},
	}
}

// Builds on Linux and macOS. Windows uses published builds.
func posix(h *v1.HostProfile) bool { return h.GetOs() != "windows" }

func (LlamaCpp) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "CUDA kernels for the probed compute capabilities",
			Tools:       []string{"nvcc"},
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "nvidia") },
			Vars: func(h *v1.HostProfile) map[string]string {
				return map[string]string{"backend": "-DGGML_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=" + cudaArchitectures(h)}
			},
		},
		{
			ID:          "rocm",
			Description: "HIP kernels for AMD devices",
			Tools:       []string{"hipcc"},
			Requires:    "an AMD device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "amd") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_HIP=ON"} },
		},
		{
			ID:          "metal",
			Description: "Metal on Apple silicon",
			Requires:    "Apple silicon",
			Applies:     func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_METAL=ON"} },
		},
		{
			ID:          "vulkan",
			Description: "Vulkan shaders, works on any GPU with a Vulkan driver",
			Tools:       []string{"glslc"},
			Requires:    "a GPU",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasGPU(h) },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_VULKAN=ON"} },
		},
		{
			ID:          "cpu",
			Description: "CPU only with native instruction selection",
			Applies:     posix,
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_NATIVE=ON"} },
		},
	}
}

// Formats NVIDIA compute capabilities for CMake, such as 8.6 to 86.
func cudaArchitectures(h *v1.HostProfile) string {
	var out []string
	for _, d := range host.Vendor(h, "nvidia") {
		if cc := strings.ReplaceAll(d.GetFacts()["compute_capability"], ".", ""); cc != "" {
			out = append(out, cc)
		}
	}
	return strings.Join(out, ";")
}

func (LlamaCpp) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

func (LlamaCpp) Steps(b *Build) []Step {
	configure := command("cmake", "-S", ".", "-B", "build",
		"-DCMAKE_BUILD_TYPE="+arg(b.Vars, "build_type"), arg(b.Vars, "backend"), arg(b.Vars, "archs"),
		"-DBUILD_SHARED_LIBS=OFF", "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF", "-DLLAMA_BUILD_SERVER=ON", "-DLLAMA_BUILD_TOOLS=ON", "-DGGML_RPC=ON")
	configure = append(configure, args(b.Vars, "extra")...)
	return []Step{
		{Name: "configure", Command: configure},
		{Name: "compile", Command: command("cmake", "--build", "build", "--config", arg(b.Vars, "build_type"), "-j", strconv.Itoa(b.Jobs))},
	}
}

// The server and the rpc server under whatever name this revision builds it
func (LlamaCpp) Outputs() []string {
	return []string{"build/bin/llama-server", "build/bin/*rpc-server*"}
}
func (LlamaCpp) Binary() string         { return "llama-server" }
func (LlamaCpp) Timeout() time.Duration { return 3 * time.Hour }

// Yields a slot whose cache a request would mostly discard to an empty slot, so conversations stop evicting each other
const llamaEmptySlotFirst = `--- a/tools/server/server-context.cpp
+++ b/tools/server/server-context.cpp
@@ -1856,9 +1856,21 @@
                             f_sim_best, slot_prompt_similarity, f_keep);
                 }
 
                 // if we are about to lose a large portion of the existing context - save it in the prompt cache
                 if (f_keep < 0.5f) {
                     update_cache = true;
                 }
+
+                // a weak match would evict most of this slot's conversation while an empty slot sits idle,
+                // so the empty slot takes the request and this cache stays with its own conversation
+                if (f_keep < 0.5f && task.id_slot == -1) {
+                    for (server_slot & slot : slots) {
+                        if (!slot.is_processing() && slot.prompt.tokens.empty()) {
+                            SLT_INF(slot, "selected empty slot over a weak match, f_sim_best = %.3f, f_keep = %.3f\n", f_sim_best, f_keep);
+                            ret = nullptr;
+                            break;
+                        }
+                    }
+                }
             }
         }
`

// Keeps idle slots of a unified cache in place, the least recently used moving to the prompt cache for room
const llamaResidentIdleSlots = `--- a/tools/server/server-context.cpp
+++ b/tools/server/server-context.cpp
@@ -1666,7 +1666,7 @@
                 params_base.cache_idle_slots = false;
             } else {
                 if (params_base.kv_unified) {
-                    SRV_TRC("%s", "idle slots will be saved to prompt cache and cleared upon starting a new task\n");
+                    SRV_TRC("%s", "idle slots keep their cells until a running slot needs them, then move to the prompt cache\n");
                 } else {
                     // without a unified KV cache, clearing a slot frees no reusable room, so we only
                     // publish a RAM-cache copy of idle slots (their KV stays in VRAM) [TAG_IDLE_SLOT_CLEAR]
@@ -1902,8 +1902,32 @@
 
                 ret->prompt_save(*prompt_cache);
 
+                // a unified KV cache holds the prompt only in the cells other slots leave it, so idle ones move to
+                // the prompt cache, the least recently used first, before the state cached for this prompt comes back
+                while (params_base.kv_unified) {
+                    size_t n_held = 0;
+                    for (auto & slot : slots) {
+                        if (&slot != ret) {
+                            n_held += slot.prompt.n_tokens();
+                        }
+                    }
+
+                    if (n_held + task.tokens.size() <= (size_t) n_ctx || !try_clear_idle_slots(ret)) {
+                        break;
+                    }
+                }
+
                 if (!ret->prompt_load(*prompt_cache, task.tokens)) {
                     ret->prompt_clear();
+
+                    // a cached state longer than the prompt needs more cells than the prompt does
+                    while (try_clear_idle_slots(ret)) {
+                        if (ret->prompt_load(*prompt_cache, task.tokens)) {
+                            break;
+                        }
+
+                        ret->prompt_clear();
+                    }
                 }
 
                 prompt_cache->update();
@@ -1915,36 +1939,42 @@
         return ret;
     }
 
-    // return true if at least one slot has been cleared
-    // TODO: improve logic
-    //       - smarter decision which slot to clear (LRU or longest prompt?)
-    //       - move slot to level 2 cache instead of removing?
-    //       - instead of purging, try to store and resume later?
-    bool try_clear_idle_slots() {
-        bool res = false;
-
+    // return true if a slot has been cleared
+    // the idle slot used longest ago, other than the one to keep, gives up its cells, its state moved to
+    // the prompt cache so its conversation resumes from there instead of being processed again
+    bool try_clear_idle_slots(const server_slot * keep = nullptr) {
         if (!params_base.kv_unified) {
-            return res;
+            return false;
         }
 
+        server_slot * lru = nullptr;
+
         for (auto & slot : slots) {
-            if (slot.is_processing()) {
+            if (&slot == keep || slot.is_processing() || slot.prompt.n_tokens() == 0) {
                 continue;
             }
 
-            if (slot.prompt.n_tokens() > 0) {
-                SRV_WRN("purging slot %d with %zu tokens\n", slot.id, slot.prompt.tokens.size());
+            if (lru == nullptr || slot.t_last_used < lru->t_last_used) {
+                lru = &slot;
+            }
+        }
 
-                slot.prompt_clear();
+        if (lru == nullptr) {
+            return false;
+        }
 
-                res = true;
+        // clear slots one by one
+        if (prompt_cache && lru->prompt_save(*prompt_cache)) {
+            prompt_cache->update();
 
-                // clear slots one by one
-                break;
-            }
+            SRV_WRN("moved slot %d with %zu tokens to the prompt cache\n", lru->id, lru->prompt.tokens.size());
+        } else {
+            SRV_WRN("clearing slot %d with %zu tokens, the prompt cache holds its state already or cannot take it\n", lru->id, lru->prompt.tokens.size());
         }
 
-        return res;
+        lru->prompt_clear();
+
+        return true;
     }
 
     std::vector<common_adapter_lora_info> construct_lora_list(const std::map<int, float> & config) const {
@@ -3080,7 +3110,9 @@
                         break; // drop the task
                     }
 
-                    if (params_base.cache_idle_slots) {
+                    // with a unified KV cache an idle slot keeps its cells and its conversation resumes in place,
+                    // try_clear_idle_slots moving it to the prompt cache once a running slot needs the room
+                    if (params_base.cache_idle_slots && !params_base.kv_unified) {
                         for (auto & slot : slots) {
                             if (!slot.is_processing()) {
                                 SLT_TRC(slot, "%s", "saving idle slot to prompt cache\n");
@@ -3089,11 +3121,6 @@
                                     SLT_DBG(slot, "%s", "__TEST_TAG_CACHE_IDLE_SLOT__\n");
                                     prompt_cache->update();
                                 }
-
-                                if (params_base.kv_unified) {
-                                    // [TAG_IDLE_SLOT_CLEAR]
-                                    slot.prompt_clear();
-                                }
                             }
                         }
                     }
`

func (LlamaCpp) Patches() []Patch {
	return []Patch{
		{ID: "server-empty-slot-first", Description: "A request that would discard most of a slot's cache takes an empty slot instead", Content: []byte(llamaEmptySlotFirst)},
		{ID: "server-resident-idle-slots", Description: "Idle slots of a unified cache keep their cells until a running slot needs the room", Content: []byte(llamaResidentIdleSlots)},
	}
}
