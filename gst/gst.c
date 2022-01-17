
#include "gst.h"

#include <gst/app/gstappsrc.h>

GMainLoop *gstreamer_main_loop = NULL;

static gboolean
gstreamer_bus_call(GstBus *bus, GstMessage *msg, gpointer data)
{
	switch (GST_MESSAGE_TYPE(msg)) {
	case GST_MESSAGE_EOS:
		g_print("End of stream\n");
		exit(1);
		break;

	case GST_MESSAGE_ERROR:;
		gchar *debug;
		GError *error;

		gst_message_parse_error(msg, &error, &debug);
		g_free(debug);

		g_printerr("Error: %s\n", error->message);
		g_error_free(error);
		exit(1);

	default:
		break;
	}

	return TRUE;
}

void
gstreamer_start_mainloop(void)
{
	gstreamer_main_loop = g_main_loop_new(NULL, FALSE);

	g_main_loop_run(gstreamer_main_loop);
}

GstElement *
gstreamer_create_pipeline(char *pipeline)
{
	gst_init(NULL, NULL);
	GError *error = NULL;
	return gst_parse_launch(pipeline, &error);
}

/* Receive */

void
gstreamer_receive_start_pipeline(GstElement *pipeline)
{
	GstBus *bus = gst_pipeline_get_bus(GST_PIPELINE(pipeline));
	gst_bus_add_watch(bus, gstreamer_bus_call, NULL);
	gst_object_unref(bus);

	gst_element_set_state(pipeline, GST_STATE_PLAYING);
}

void
gstreamer_receive_stop_pipeline(GstElement *pipeline)
{
	gst_element_set_state(pipeline, GST_STATE_NULL);
}

void
gstreamer_receive_push_buffer(GstElement *pipeline, void *buffer, int len)
{
	GstElement *src = gst_bin_get_by_name(GST_BIN(pipeline), "src");
	if (src != NULL) {
		/* Used to use g_memdup */
		gpointer p = g_memdup2(buffer, len);
		GstBuffer *buffer = gst_buffer_new_wrapped(p, len);
		gst_app_src_push_buffer(GST_APP_SRC(src), buffer);
		gst_object_unref(src);
	}
}

