package recipes

import (
	"strconv"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Builds sd-server with the host backend.
type SDCpp struct{}

func (SDCpp) ID() string        { return "sdcpp" }
func (SDCpp) RuntimeID() string { return "sdcpp" }
func (SDCpp) Description() string {
	return "sd-server compiled from a tag, branch, or commit of stable-diffusion.cpp"
}

// Clone to include ggml and image codec submodules.
func (SDCpp) Source() Source {
	return Source{Releases: "leejet/stable-diffusion.cpp", Repo: "https://github.com/leejet/stable-diffusion.cpp"}
}

func (SDCpp) Tools() []string { return []string{"git", "cmake", "cc", "c++"} }
func (SDCpp) Facts() []string {
	return []string{"device.vendor", "device.compute_capability", "device.driver_version"}
}
func (SDCpp) Vars() []Var {
	return []Var{
		{Name: "build_type", Label: "Build type", Default: "Release", Description: "The cmake build type", Choices: []string{"Release", "RelWithDebInfo", "Debug", "MinSizeRel"}},
		{Name: "extra", Label: "Extra cmake flags", Description: "Space-separated CMake flags, such as -DGGML_CUDA_F16=ON"},
	}
}

func (SDCpp) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "CUDA kernels for the probed compute capabilities",
			Tools:       []string{"nvcc"},
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "nvidia") },
			Vars: func(h *v1.HostProfile) map[string]string {
				return map[string]string{"backend": "-DSD_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=" + cudaArchitectures(h)}
			},
		},
		{
			ID:          "rocm",
			Description: "HIP kernels for AMD devices",
			Tools:       []string{"hipcc"},
			Requires:    "an AMD device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "amd") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_HIPBLAS=ON"} },
		},
		{
			ID:          "metal",
			Description: "Metal on Apple silicon",
			Requires:    "Apple silicon",
			Applies:     func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_METAL=ON"} },
		},
		{
			ID:          "vulkan",
			Description: "Vulkan shaders, works on any GPU with a Vulkan driver",
			Tools:       []string{"glslc"},
			Requires:    "a GPU",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasGPU(h) },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_VULKAN=ON"} },
		},
		{
			ID:          "cpu",
			Description: "CPU only with native instruction selection",
			Applies:     posix,
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_NATIVE=ON"} },
		},
	}
}

func (SDCpp) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

// Fetch submodules and build a static server without the bundled web page.
func (SDCpp) Steps(b *Build) []Step {
	configure := command("cmake", "-S", ".", "-B", "build",
		"-DCMAKE_BUILD_TYPE="+arg(b.Vars, "build_type"), arg(b.Vars, "backend"), arg(b.Vars, "archs"),
		"-DSD_BUILD_SHARED_LIBS=OFF", "-DSD_BUILD_EXAMPLES=ON", "-DSD_SERVER_BUILD_FRONTEND=OFF", "-DGGML_RPC=ON")
	configure = append(configure, args(b.Vars, "extra")...)
	return []Step{
		{Name: "submodules", Command: command("git", "submodule", "update", "--init", "--recursive", "--depth", "1")},
		{Name: "configure", Command: configure},
		{Name: "compile", Command: command("cmake", "--build", "build", "--config", arg(b.Vars, "build_type"), "-j", strconv.Itoa(b.Jobs))},
	}
}

// The server and the rpc server that lets it put its denoiser on another node
func (SDCpp) Outputs() []string      { return []string{"build/bin/sd-server", "build/bin/*rpc-server*"} }
func (SDCpp) Binary() string         { return "sd-server" }
func (SDCpp) Timeout() time.Duration { return 3 * time.Hour }

const sdcppQwen3VLEmbedTokens = `--- a/src/name_conversion.cpp
+++ b/src/name_conversion.cpp
@@ -758,6 +758,7 @@
 std::string convert_llada2_moe_te_name(std::string name) {
     static const std::vector<std::pair<std::string, std::string>> name_map = {
         {"model.language_model.word_embeddings.", "model.embed_tokens."},
+        {"model.language_model.embed_tokens.", "model.embed_tokens."},
         {"model.language_model.norm.", "model.norm."},
         {"model.language_model.lm_head.", "lm_head."},
         {"model.language_model.layers.", "model.layers."},
`

// sd-server refuses to cancel a job once it is generating
const sdcppServerCancelGenerating = `--- a/examples/server/async_jobs.h
+++ b/examples/server/async_jobs.h
@@ -44,6 +44,7 @@
     int result_fps         = 0;
     std::string error_code;
     std::string error_message;
+    bool cancel_requested = false;
 };
 
 struct AsyncJobManager {
--- a/examples/server/async_jobs.cpp
+++ b/examples/server/async_jobs.cpp
@@ -306,9 +306,13 @@
                 continue;
             }
 
-            job             = it->second;
-            job->status     = AsyncJobStatus::Generating;
-            job->started_at = unix_timestamp_now();
+            job                   = it->second;
+            job->status           = AsyncJobStatus::Generating;
+            job->started_at       = unix_timestamp_now();
+            job->cancel_requested = false;
+            // The manager mutex orders this reset before any cancel that sees the job
+            // generating, so a cancel for this job is never cleared by a stale reset.
+            sd_cancel_generation(runtime.sd_ctx, SD_CANCEL_RESET);
         }
 
         std::vector<std::string> output_images;
@@ -350,6 +354,15 @@
                 job->result_fps             = output_fps;
                 job->error_code.clear();
                 job->error_message.clear();
+            } else if (job->cancel_requested) {
+                job->status        = AsyncJobStatus::Cancelled;
+                job->error_code    = "cancelled";
+                job->error_message = "job cancelled by client";
+                job->result_images_b64.clear();
+                job->result_media_b64.clear();
+                job->result_media_mime_type.clear();
+                job->result_frame_count = 0;
+                job->result_fps         = 0;
             } else {
                 job->status        = AsyncJobStatus::Failed;
                 job->error_code    = "generation_failed";
--- a/examples/server/routes_sdcpp.cpp
+++ b/examples/server/routes_sdcpp.cpp
@@ -183,7 +183,7 @@
         {"hires", true},
         {"cache", true},
         {"cancel_queued", true},
-        {"cancel_generating", false},
+        {"cancel_generating", true},
     };
 }
 
@@ -197,7 +197,7 @@
         {"vae_tiling", true},
         {"cache", true},
         {"cancel_queued", true},
-        {"cancel_generating", false},
+        {"cancel_generating", true},
     };
 }
 
@@ -309,7 +309,7 @@
     json top_level_output_formats = json::array();
     json top_level_features       = {
               {"cancel_queued", true},
-              {"cancel_generating", false},
+              {"cancel_generating", true},
     };
     std::string current_mode = "";
     if (supports_img) {
@@ -592,8 +592,12 @@
         }
 
         if (job.status == AsyncJobStatus::Generating) {
-            res.status = 409;
-            res.set_content(R"({"error":"job is currently generating and cannot be interrupted yet"})", "application/json");
+            // The sampler checks the flag every step and the worker marks the job
+            // cancelled once generate returns, so the client polls until then.
+            job.cancel_requested = true;
+            sd_cancel_generation(runtime->sd_ctx, SD_CANCEL_ALL);
+            res.status = 202;
+            res.set_content(make_async_job_json(manager, job).dump(), "application/json");
             return;
         }
 
--- a/src/pipeline/image.cpp
+++ b/src/pipeline/image.cpp
@@ -793,8 +793,6 @@
             return false;
         }
 
-        sd->reset_cancel_flag();
-
         int64_t t0            = ggml_time_ms();
         sd->vae_tiling_params = sd_img_gen_params->vae_tiling_params;
         GenerationRequest request(sd, sd_img_gen_params);
--- a/src/pipeline/video.cpp
+++ b/src/pipeline/video.cpp
@@ -1548,8 +1548,6 @@
             return generate_animatediff_video(sd, sd_vid_gen_params, frames_out, num_frames_out);
         }
 
-        sd->reset_cancel_flag();
-
         const RefImageParams ref_image_params;
 
         int64_t t0            = ggml_time_ms();
`

func (SDCpp) Patches() []Patch {
	return []Patch{
		{ID: "qwen3-vl-hf-embed-tokens", Content: []byte(sdcppQwen3VLEmbedTokens)},
		{ID: "server-cancel-generating", Content: []byte(sdcppServerCancelGenerating)},
	}
}
