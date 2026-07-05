package com.proxyma.android.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

val DeepGray = Color(0xFF121214)
val CardGray = Color(0xFF1E1E24)
val VioletPrimary = Color(0xFF9D4EDD)
val VioletSecondary = Color(0xFFE0AAFF)
val MintGreen = Color(0xFF52B788)
val AmberWarning = Color(0xFFF3C052)
val ErrorRed = Color(0xFFE63946)

@Composable
fun ProxymaAppTheme(content: @Composable () -> Unit) {
    val colorScheme = darkColorScheme(
        primary = VioletPrimary,
        secondary = VioletSecondary,
        background = DeepGray,
        surface = CardGray,
        error = ErrorRed
    )

    MaterialTheme(
        colorScheme = colorScheme,
        content = content
    )
}
