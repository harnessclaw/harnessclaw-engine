package videogen

const videoCreateDescription = "Submit a text-to-video or image-to-video generation task to the configured video model. " +
	"Returns a task_id immediately; the video is NOT ready yet. " +
	"Call video_query with the returned task_id to poll for the finished video file. " +
	"For image-to-video, pass image_url or image_b64 (the first frame)."

const videoQueryDescription = "Poll a video generation task by task_id. Blocks up to timeout_s seconds (default 3, polling once per second). " +
	"On success returns the local video_path. If the task is still running when the timeout elapses it returns status=\"running\" — " +
	"call video_query again with the same task_id. On failure/expiry it returns a status and a hint describing how to retry."
