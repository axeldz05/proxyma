package com.proxyma.android.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.ui.theme.CardGray
import com.proxyma.android.ui.theme.MintGreen

@Composable
fun Icon(
    imageVector: androidx.compose.ui.graphics.vector.ImageVector,
    contentDescription: String,
    tint: Color,
    size: Dp
) {
    Icon(
        imageVector = imageVector,
        contentDescription = contentDescription,
        tint = tint,
        modifier = Modifier.size(size)
    )
}

@Composable
fun StatusIndicator(
    active: Boolean,
    activeLabel: String,
    inactiveLabel: String,
    activeColor: Color = MintGreen,
    inactiveColor: Color = Color.Gray,
    dotSize: Dp = 8.dp,
    fontSize: TextUnit = 12.sp,
    fontWeight: FontWeight = FontWeight.Normal
) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(
            modifier = Modifier
                .size(dotSize)
                .clip(RoundedCornerShape(dotSize / 2))
                .background(if (active) activeColor else inactiveColor)
        )
        Spacer(Modifier.width(dotSize / 2 + 2.dp))
        Text(
            text = if (active) activeLabel else inactiveLabel,
            color = if (active) activeColor else inactiveColor,
            fontSize = fontSize,
            fontWeight = fontWeight
        )
    }
}

@Composable
fun ScreenTitle(
    text: String,
    modifier: Modifier = Modifier
) {
    Text(
        text = text,
        fontSize = 24.sp,
        fontWeight = FontWeight.Bold,
        color = Color.White,
        modifier = modifier
    )
}

@Composable
fun ProxymaCard(
    modifier: Modifier = Modifier,
    shape: androidx.compose.ui.graphics.Shape = RoundedCornerShape(12.dp),
    content: @Composable ColumnScope.() -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = CardGray),
        shape = shape,
        modifier = modifier.fillMaxWidth(),
        content = content
    )
}
