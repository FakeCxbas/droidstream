package com.fakecxbas.droidstream.camera

import android.content.Context
import android.graphics.ImageFormat
import android.graphics.Rect
import android.graphics.YuvImage
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CaptureRequest
import android.util.Range
import android.util.Size
import androidx.annotation.OptIn
import androidx.camera.camera2.interop.Camera2CameraInfo
import androidx.camera.camera2.interop.Camera2Interop
import androidx.camera.camera2.interop.ExperimentalCamera2Interop
import androidx.camera.core.*
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import java.io.ByteArrayOutputStream
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

class CameraStreamManager(
    private val context: Context,
    private val lifecycleOwner: LifecycleOwner,
    private val previewView: PreviewView?,
    private val onFrameReady: (ByteArray, Int, Int) -> Unit
) {
    private var cameraProvider: ProcessCameraProvider? = null
    private var camera: Camera? = null
    private var imageAnalysis: ImageAnalysis? = null
    private val cameraExecutor: ExecutorService = Executors.newSingleThreadExecutor()

    var lensFacing: Int = CameraSelector.LENS_FACING_BACK
        private set

    var targetWidth = 640
    var targetHeight = 480
    var jpegQuality = 80
    var manualRotation = 0 // 0, 90, 180, 270
    var targetFps = 30 // 30 or 60

    fun rotateNext(): Int {
        manualRotation = (manualRotation + 90) % 360
        return manualRotation
    }

    fun setResolution(width: Int, height: Int) {
        targetWidth = width
        targetHeight = height
        bindCameraUseCases()
    }

    fun setFps(fps: Int) {
        targetFps = fps
        if (fps >= 60 && targetWidth == 640 && targetHeight == 480) {
            // Galaxy S21 front camera supports 60 FPS in 16:9 mode (720p/1080p)
            targetWidth = 1280
            targetHeight = 720
        }
        bindCameraUseCases()
    }

    @OptIn(ExperimentalCamera2Interop::class)
    fun getAvailableFpsRanges(): List<Range<Int>> {
        val info = camera?.cameraInfo ?: return emptyList()
        val camera2Info = Camera2CameraInfo.from(info)
        val ranges = camera2Info.getCameraCharacteristic(CameraCharacteristics.CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES)
        return ranges?.toList() ?: emptyList()
    }

    private fun findBestFpsRange(target: Int): Range<Int> {
        val ranges = getAvailableFpsRanges()
        for (r in ranges) {
            if (r.lower == target && r.upper == target) return r
        }
        for (r in ranges) {
            if (r.upper == target) return r
        }
        for (r in ranges) {
            if (r.upper >= target) return r
        }
        return if (target >= 60) Range(30, 60) else Range(15, 30)
    }

    fun startCamera(onReady: () -> Unit = {}) {
        val cameraProviderFuture = ProcessCameraProvider.getInstance(context)
        cameraProviderFuture.addListener({
            cameraProvider = cameraProviderFuture.get()
            bindCameraUseCases()
            onReady()
        }, ContextCompat.getMainExecutor(context))
    }

    @OptIn(ExperimentalCamera2Interop::class)
    private fun bindCameraUseCases() {
        val provider = cameraProvider ?: return
        provider.unbindAll()

        val cameraSelector = CameraSelector.Builder()
            .requireLensFacing(lensFacing)
            .build()

        if (!provider.hasCamera(cameraSelector)) {
            lensFacing = if (lensFacing == CameraSelector.LENS_FACING_BACK) {
                CameraSelector.LENS_FACING_FRONT
            } else {
                CameraSelector.LENS_FACING_BACK
            }
        }

        val effectiveSelector = CameraSelector.Builder()
            .requireLensFacing(lensFacing)
            .build()

        val previewBuilder = Preview.Builder()
            .setTargetResolution(Size(targetWidth, targetHeight))

        val analysisBuilder = ImageAnalysis.Builder()
            .setTargetResolution(Size(targetWidth, targetHeight))
            .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
            .setOutputImageFormat(ImageAnalysis.OUTPUT_IMAGE_FORMAT_YUV_420_888)

        // Configure target FPS if supported
        val selectedRange = findBestFpsRange(targetFps)
        if (selectedRange != null) {
            Camera2Interop.Extender(previewBuilder)
                .setCaptureRequestOption(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, selectedRange)
            Camera2Interop.Extender(analysisBuilder)
                .setCaptureRequestOption(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, selectedRange)
        }

        val preview = previewBuilder.build()
        previewView?.let {
            preview.setSurfaceProvider(it.surfaceProvider)
        }

        imageAnalysis = analysisBuilder.build().also { analysis ->
            analysis.setAnalyzer(cameraExecutor) { imageProxy ->
                processImageProxy(imageProxy)
            }
        }

        camera = if (previewView != null) {
            provider.bindToLifecycle(lifecycleOwner, effectiveSelector, preview, imageAnalysis)
        } else {
            provider.bindToLifecycle(lifecycleOwner, effectiveSelector, imageAnalysis)
        }
    }

    private fun processImageProxy(image: ImageProxy) {
        val sensorRotation = image.imageInfo.rotationDegrees
        val totalRotation = (sensorRotation + manualRotation) % 360

        val rawNv21 = yuv420ToNv21(image)
        val (finalNv21, finalSize) = rotateNV21(rawNv21, image.width, image.height, totalRotation)

        val out = ByteArrayOutputStream()
        val yuvImage = YuvImage(finalNv21, ImageFormat.NV21, finalSize.width, finalSize.height, null)
        yuvImage.compressToJpeg(Rect(0, 0, finalSize.width, finalSize.height), jpegQuality, out)
        val jpegBytes = out.toByteArray()

        onFrameReady(jpegBytes, finalSize.width, finalSize.height)
        image.close()
    }

    private fun rotateNV21(input: ByteArray, width: Int, height: Int, rotation: Int): Pair<ByteArray, Size> {
        if (rotation == 0) return Pair(input, Size(width, height))
        val ySize = width * height
        val output = ByteArray(input.size)

        when (rotation) {
            90 -> {
                var i = 0
                for (x in 0 until width) {
                    for (y in height - 1 downTo 0) {
                        output[i++] = input[y * width + x]
                    }
                }
                var uv = ySize
                for (x in 0 until width step 2) {
                    for (y in height / 2 - 1 downTo 0) {
                        val p = ySize + y * width + x
                        output[uv++] = input[p]     // V
                        output[uv++] = input[p + 1] // U
                    }
                }
                return Pair(output, Size(height, width))
            }
            270 -> {
                var i = 0
                for (x in width - 1 downTo 0) {
                    for (y in 0 until height) {
                        output[i++] = input[y * width + x]
                    }
                }
                var uv = ySize
                for (x in width - 2 downTo 0 step 2) {
                    for (y in 0 until height / 2) {
                        val p = ySize + y * width + x
                        output[uv++] = input[p]     // V
                        output[uv++] = input[p + 1] // U
                    }
                }
                return Pair(output, Size(height, width))
            }
            180 -> {
                var i = 0
                for (y in height - 1 downTo 0) {
                    for (x in width - 1 downTo 0) {
                        output[i++] = input[y * width + x]
                    }
                }
                var uv = ySize
                for (y in height / 2 - 1 downTo 0) {
                    for (x in width - 2 downTo 0 step 2) {
                        val p = ySize + y * width + x
                        output[uv++] = input[p]
                        output[uv++] = input[p + 1]
                    }
                }
                return Pair(output, Size(width, height))
            }
            else -> return Pair(input, Size(width, height))
        }
    }

    private fun yuv420ToNv21(image: ImageProxy): ByteArray {
        val width = image.width
        val height = image.height
        val ySize = width * height
        val uvSize = width * height / 2
        val nv21 = ByteArray(ySize + uvSize)

        val yPlane = image.planes[0]
        val uPlane = image.planes[1]
        val vPlane = image.planes[2]

        val yBuffer = yPlane.buffer
        val uBuffer = uPlane.buffer
        val vBuffer = vPlane.buffer

        val yRowStride = yPlane.rowStride
        var pos = 0

        if (yRowStride == width) {
            yBuffer.get(nv21, 0, ySize)
            pos = ySize
        } else {
            for (row in 0 until height) {
                yBuffer.position(row * yRowStride)
                yBuffer.get(nv21, pos, width)
                pos += width
            }
        }

        val vRowStride = vPlane.rowStride
        val vPixelStride = vPlane.pixelStride
        val uRowStride = uPlane.rowStride
        val uPixelStride = uPlane.pixelStride

        val uvHeight = height / 2
        val uvWidth = width / 2

        for (row in 0 until uvHeight) {
            for (col in 0 until uvWidth) {
                val vIndex = row * vRowStride + col * vPixelStride
                val uIndex = row * uRowStride + col * uPixelStride

                nv21[pos++] = vBuffer.get(vIndex)
                nv21[pos++] = uBuffer.get(uIndex)
            }
        }

        return nv21
    }

    fun switchCamera(): Int {
        val provider = cameraProvider ?: return lensFacing
        val nextFacing = if (lensFacing == CameraSelector.LENS_FACING_BACK) {
            CameraSelector.LENS_FACING_FRONT
        } else {
            CameraSelector.LENS_FACING_BACK
        }

        val selector = CameraSelector.Builder()
            .requireLensFacing(nextFacing)
            .build()

        if (provider.hasCamera(selector)) {
            lensFacing = nextFacing
            bindCameraUseCases()
        }

        return lensFacing
    }

    fun hasFlashUnit(): Boolean {
        return camera?.cameraInfo?.hasFlashUnit() == true
    }

    fun setTorch(enable: Boolean): Boolean {
        if (!hasFlashUnit()) {
            return false
        }
        camera?.cameraControl?.enableTorch(enable)
        return true
    }

    fun stop() {
        cameraProvider?.unbindAll()
        cameraExecutor.shutdown()
    }
}
