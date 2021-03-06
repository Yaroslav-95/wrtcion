#ifndef GST_H
#define GST_H

#include <glib.h>
#include <gst/gst.h>
#include <stdint.h>
#include <stdlib.h>

void gstreamer_start_mainloop(void);
GstElement *gstreamer_create_pipeline(char *pipeline);

/* Receive */

void gstreamer_receive_start_pipeline(GstElement *pipeline);
void gstreamer_receive_stop_pipeline(GstElement *pipeline);
void gstreamer_receive_push_buffer(GstElement *pipeline, void *buffer, int len);

#endif
