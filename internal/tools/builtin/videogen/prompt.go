package videogen

const videoCreateDescription = "提交一个文生视频或图生视频任务到已配置的视频模型。" +
	"立即返回 task_id —— **视频尚未生成完毕**，必须用 video_query 轮询直到完成才能拿到 video_path。" +
	"图生视频时通过 image_url 或 image_path（本地路径，原样传入，不要转 base64）指定首帧。"

const videoQueryDescription = "用 task_id 轮询视频生成任务状态。在 timeout_s 秒内（默认 3 秒，每秒 poll 一次）阻塞等待。" +
	"成功时返回本地 video_path。若超时仍未完成返回 status=\"running\"，请用同一个 task_id 再次调用本工具继续轮询。" +
	"失败 / 过期时返回 status 与重试 hint。视频生成耗时较长（数分钟级），建议 timeout_s 给 10 左右合理值，不要每秒打一次空响应。"
