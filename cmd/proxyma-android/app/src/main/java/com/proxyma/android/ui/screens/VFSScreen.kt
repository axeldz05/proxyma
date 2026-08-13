package com.proxyma.android.ui.screens

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.Composable
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.proxyma.android.models.UIDomain
import com.proxyma.android.ui.components.AdminActionCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.utils.*

@Composable
fun VFSScreen(storageDomain: UIDomain?) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            ScreenTitle(storageDomain?.title ?: "VFS File Manager")
        }
        storageDomain?.actions?.forEach { action ->
            item(key = action.key) {
                AdminActionCard(
                    action = action,
                    localFilePath = action.name == "upload"
                ) { selected, inputs, onComplete ->
                    if (selected.name == "open") {
                        val name = inputs["name"]?.toString().orEmpty()
                        runOnBg(scope, action = {
                            val localPath = proxyma_bind.Proxyma_bind.resolveLocalBlob(name)
                            bindResult(localPath, BindMethod.LEGACY_ERROR_PREFIX).getOrThrow()
                        }) { result ->
                            result.onSuccess { localPath ->
                                openFileNatively(scope, context, localPath, name)
                            }
                            onComplete(result.map { "Opened '$name'" })
                        }
                    } else {
                        submitUIAction(scope, selected, inputs, onComplete)
                    }
                }
            }
        }
    }
}
