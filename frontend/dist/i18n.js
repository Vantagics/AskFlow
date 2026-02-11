// ============================================================
// i18n - Internationalization Support (Chinese / English)
// ============================================================

(function () {
    'use strict';

    var LANG_KEY = 'helpdesk_lang';

    var translations = {
        'zh-CN': {
            // Page title
            'site_title': '软件自助服务平台',

            // Login page
            'login_title': '软件自助服务平台',
            'login_subtitle': '登录您的账号',
            'login_email': '邮箱',
            'login_password': '密码',
            'login_captcha': '验证码答案',
            'login_btn': '登录',
            'login_no_account': '还没有账号？',
            'login_register_link': '注册',
            'login_error_email_password': '请输入邮箱和密码',
            'login_error_captcha': '请输入验证码',
            'login_failed': '登录失败',
            'captcha_load_fail': '加载失败，点击重试',

            // Register page
            'register_subtitle': '创建新账号',
            'register_name': '昵称',
            'register_email': '邮箱',
            'register_password': '密码（至少6位）',
            'register_password_confirm': '确认密码',
            'register_captcha': '验证码答案',
            'register_btn': '注册',
            'register_has_account': '已有账号？',
            'register_login_link': '登录',
            'register_error_email': '请输入邮箱',
            'register_error_password': '请输入密码',
            'register_error_password_length': '密码至少6位',
            'register_error_password_mismatch': '两次密码不一致',
            'register_error_captcha': '请输入验证码',
            'register_failed': '注册失败',
            'register_success': '注册成功，请查收验证邮件',

            // Verify page
            'verify_title': '邮箱验证',
            'verify_loading': '正在验证...',
            'verify_invalid_link': '无效的验证链接',
            'verify_failed': '验证失败',
            'verify_success': '邮箱验证成功',
            'verify_go_login': '前往登录',

            // Admin login
            'admin_login_title': '管理员登录',
            'admin_username': '管理员用户名',
            'admin_password': '管理员密码',
            'admin_login_btn': '登录',
            'admin_setup_hint': '首次使用，请设置管理员账号',
            'admin_setup_username': '设置管理员用户名',
            'admin_setup_password': '设置管理员密码',
            'admin_setup_password_confirm': '确认密码',
            'admin_setup_btn': '创建管理员',
            'admin_error_username': '请输入用户名',
            'admin_error_password': '请输入密码',
            'admin_error_password_length': '密码至少6位',
            'admin_error_password_mismatch': '两次密码不一致',
            'admin_error_credentials': '请输入用户名和密码',
            'admin_error_wrong_credentials': '用户名或密码错误',
            'admin_setup_failed': '设置失败',
            'admin_login_failed': '登录失败',
            'admin_login_retry': '登录失败，请重试',

            // Chat page
            'chat_title': '软件自助服务平台',
            'chat_login_btn': '登录',
            'chat_logout_btn': '退出登录',
            'chat_welcome_title': '欢迎使用软件自助服务平台',
            'chat_welcome_desc': '请输入您的问题，我将为您查找相关资料并提供解答。',
            'chat_input_placeholder': '输入您的问题...（可粘贴图片）',
            'chat_image_preview_alt': '预览',
            'chat_image_remove_title': '移除图片',
            'chat_image_recognize': '请识别这张图片的内容',
            'chat_request_failed': '请求失败',
            'chat_no_answer': '暂无回答',
            'chat_pending_message': '该问题已转交人工处理，请稍后查看回复',
            'chat_error_prefix': '抱歉，请求出错：',
            'chat_error_suffix': '。请稍后重试。',
            'chat_error_unknown': '未知错误',
            'chat_user_image_alt': '用户图片',
            'chat_source_toggle': '引用来源',
            'chat_source_unknown': '未知文档',
            'chat_source_image': '📷 图片来源',

            // Admin panel - sidebar
            'admin_panel_title': '管理面板',
            'admin_nav_documents': '文档管理',
            'admin_nav_pending': '待回答问题',
            'admin_nav_knowledge': '知识录入',
            'admin_nav_settings': '系统设置',
            'admin_nav_users': '用户管理',
            'admin_sidebar_logout': '退出登录',

            // Admin - documents
            'admin_doc_title': '文档管理',
            'admin_doc_drop_text': '拖拽文件到此处，或点击选择文件',
            'admin_doc_drop_hint': '支持 PDF、Word、Excel、PPT、Markdown 格式',
            'admin_doc_url_placeholder': '输入文档URL地址',
            'admin_doc_url_submit': '提交URL',
            'admin_doc_list_title': '文档列表',
            'admin_doc_th_name': '文档名称',
            'admin_doc_th_type': '文件类型',
            'admin_doc_th_status': '处理状态',
            'admin_doc_th_time': '上传时间',
            'admin_doc_th_action': '操作',
            'admin_doc_empty': '暂无文档',
            'admin_doc_status_processing': '处理中',
            'admin_doc_status_success': '成功',
            'admin_doc_status_failed': '失败',
            'admin_doc_delete_btn': '删除',
            'admin_doc_uploading': '正在上传 {name}...',
            'admin_doc_upload_success': '文件上传成功',
            'admin_doc_upload_failed': '上传失败',
            'admin_doc_url_empty': '请输入URL地址',
            'admin_doc_url_submitting': '正在提交URL...',
            'admin_doc_url_success': 'URL提交成功',
            'admin_doc_url_failed': '提交失败',
            'admin_doc_load_failed': '加载失败',

            // Admin - delete dialog
            'admin_delete_title': '确认删除',
            'admin_delete_msg': '确定要删除文档"{name}"吗？此操作不可撤销。',
            'admin_delete_default_msg': '确定要删除该文档吗？此操作不可撤销。',
            'admin_delete_cancel': '取消',
            'admin_delete_confirm': '删除',
            'admin_delete_success': '文档已删除',
            'admin_delete_failed': '删除失败',

            // Admin - pending questions
            'admin_pending_title': '待回答问题',
            'admin_pending_filter_all': '全部',
            'admin_pending_filter_pending': '待回答',
            'admin_pending_filter_answered': '已回答',
            'admin_pending_empty': '暂无问题',
            'admin_pending_user': '用户',
            'admin_pending_answer_prefix': '回答',
            'admin_pending_answer_btn': '回答',
            'admin_pending_edit_btn': '编辑',
            'admin_pending_delete_btn': '删除',
            'admin_pending_delete_confirm': '确定要删除这个问题吗？',
            'admin_pending_deleted': '已删除',

            // Admin - answer dialog
            'admin_answer_title': '回答问题',
            'admin_answer_text_label': '文字回答',
            'admin_answer_text_placeholder': '输入回答内容',
            'admin_answer_image_label': '图片（可选，支持粘贴/拖拽/点击上传）',
            'admin_answer_image_upload': '点击选择图片，或拖拽/粘贴图片到此处',
            'admin_answer_url_label': '相关URL（可选）',
            'admin_answer_cancel': '取消',
            'admin_answer_submit': '提交回答',
            'admin_answer_empty': '请输入回答内容或上传图片',
            'admin_answer_success': '回答已提交',
            'admin_answer_failed': '提交失败',

            // Admin - settings
            'admin_settings_title': '系统设置',
            'admin_settings_server_port': '服务端口',
            'admin_settings_http_port': 'HTTP 端口',
            'admin_settings_port_hint': '修改端口后需重启服务才能生效',
            'admin_settings_restart': '重启服务',
            'admin_settings_restart_confirm': '确定要重启服务吗？重启期间服务将短暂不可用。',
            'admin_settings_restarting': '服务正在重启，请稍候刷新页面...',
            'admin_settings_restart_failed': '重启失败',
            'admin_settings_llm': 'LLM 配置',
            'admin_settings_llm_endpoint': 'LLM 端点',
            'admin_settings_llm_model': 'LLM 模型',
            'admin_settings_api_key': 'API 密钥',
            'admin_settings_temperature': '温度',
            'admin_settings_max_tokens': '最大 Token',
            'admin_settings_embedding': 'Embedding 配置',
            'admin_settings_emb_endpoint': 'Embedding 端点',
            'admin_settings_emb_model': 'Embedding 模型',
            'admin_settings_emb_multimodal': '多模态嵌入',
            'admin_settings_emb_multimodal_no': '否（标准 /embeddings）',
            'admin_settings_emb_multimodal_yes': '是（/embeddings/multimodal）',
            'admin_settings_emb_multimodal_hint': '豆包视觉嵌入模型需开启此选项',
            'admin_settings_get_api_key': '获取 API Key',
            'admin_settings_vector': '向量配置',
            'admin_settings_chunk_size': '分块大小',
            'admin_settings_overlap': '重叠大小',
            'admin_settings_topk': 'Top-K',
            'admin_settings_threshold': '相似度阈值',
            'admin_settings_content_priority': '内容优先级',
            'admin_settings_priority_image': '优先图文（有图片的结果优先）',
            'admin_settings_priority_text': '优先纯文字（纯文本结果优先）',
            'admin_settings_priority_hint': '设置回答时优先使用图文内容还是纯文字内容',
            'admin_settings_smtp': '邮件服务器 (SMTP)',
            'admin_settings_smtp_host': 'SMTP 服务器',
            'admin_settings_smtp_port': '端口',
            'admin_settings_smtp_tls': '使用 TLS',
            'admin_settings_smtp_tls_yes': '是',
            'admin_settings_smtp_tls_no': '否',
            'admin_settings_smtp_username': '用户名',
            'admin_settings_smtp_password': '密码',
            'admin_settings_smtp_from_addr': '发件人地址',
            'admin_settings_smtp_from_name': '发件人名称',
            'admin_settings_smtp_test': '测试发送',
            'admin_settings_smtp_test_placeholder': '收件人邮箱',
            'admin_settings_smtp_test_btn': '发送测试邮件',
            'admin_settings_smtp_test_empty': '请输入收件人邮箱',
            'admin_settings_smtp_test_sending': '正在发送...',
            'admin_settings_smtp_test_success': '测试邮件已发送，请检查收件箱',
            'admin_settings_smtp_test_failed': '发送失败',
            'admin_settings_admin': '管理员设置',
            'admin_settings_login_route': '管理员登录路由',
            'admin_settings_login_route_hint': '访问此隐藏路由可进入管理员登录页面',
            'admin_settings_product_intro': '产品介绍',
            'admin_settings_product_intro_label': '欢迎信息',
            'admin_settings_product_intro_placeholder': '输入产品简介，用户登录后将作为欢迎信息显示',
            'admin_settings_product_intro_hint': '用户登录后在聊天页面显示此信息',
            'admin_settings_product_name': '产品名称',
            'admin_settings_product_name_label': '产品名称',
            'admin_settings_product_name_placeholder': '输入产品名称，如：XX自助服务系统',
            'admin_settings_product_name_hint': '设置后将自动显示在页面标题、登录页、聊天页等位置，并自动处理多语言翻译',
            'admin_settings_save': '保存设置',
            'admin_settings_no_changes': '没有需要保存的更改',
            'admin_settings_saved': '设置已保存',
            'admin_settings_save_failed': '保存失败',
            'admin_settings_load_failed': '加载配置失败',
            'admin_settings_not_set': '未设置',

            // Admin - knowledge
            'admin_knowledge_title': '知识录入',
            'admin_knowledge_legend': '录入图文知识',
            'admin_knowledge_title_label': '标题',
            'admin_knowledge_title_placeholder': '知识条目标题',
            'admin_knowledge_content_label': '内容',
            'admin_knowledge_content_placeholder': '输入知识内容（支持详细描述）',
            'admin_knowledge_image_label': '图片（可选）',
            'admin_knowledge_image_upload': '点击上传图片、拖拽图片到此处，或从剪贴板粘贴',
            'admin_knowledge_image_hint': '支持 JPG、PNG、GIF、WebP、BMP 格式',
            'admin_knowledge_submit': '提交录入',
            'admin_knowledge_empty': '请输入标题和内容',
            'admin_knowledge_submitting': '正在录入知识...',
            'admin_knowledge_success': '知识录入成功',
            'admin_knowledge_failed': '录入失败',

            // Admin - users
            'admin_users_title': '用户管理',
            'admin_users_add_legend': '添加管理员账号',
            'admin_users_username': '用户名',
            'admin_users_password': '密码（至少6位）',
            'admin_users_role': '角色',
            'admin_users_role_editor': '编辑员（内容管理/问题回答）',
            'admin_users_role_super': '超级管理员（全部权限）',
            'admin_users_role_hint': '编辑员仅可管理文档、回答问题和录入知识，不能修改系统设置和管理用户',
            'admin_users_add_btn': '添加用户',
            'admin_users_list_legend': '管理员账号列表',
            'admin_users_th_username': '用户名',
            'admin_users_th_role': '角色',
            'admin_users_th_time': '创建时间',
            'admin_users_th_action': '操作',
            'admin_users_empty': '暂无子账号',
            'admin_users_role_editor_short': '编辑员',
            'admin_users_role_super_short': '超级管理员',
            'admin_users_delete_btn': '删除',
            'admin_users_delete_confirm': '确定要删除用户"{name}"吗？',
            'admin_users_deleted': '用户已删除',
            'admin_users_create_empty': '请输入用户名和密码',
            'admin_users_create_password_short': '密码至少6位',
            'admin_users_created': '用户创建成功',
            'admin_users_create_failed': '创建失败',

            // Image upload common
            'image_select_error': '请选择图片文件',
            'image_size_error': '图片大小不能超过10MB',
            'image_upload_failed': '图片上传失败',
            'image_remove_label': '删除图片',

            // Language
            'lang_switch': 'EN'
        },

        'en-US': {
            // Page title
            'site_title': 'Software Self-Service Platform',

            // Login page
            'login_title': 'Software Self-Service Platform',
            'login_subtitle': 'Sign in to your account',
            'login_email': 'Email',
            'login_password': 'Password',
            'login_captcha': 'Captcha answer',
            'login_btn': 'Sign In',
            'login_no_account': "Don't have an account? ",
            'login_register_link': 'Sign Up',
            'login_error_email_password': 'Please enter email and password',
            'login_error_captcha': 'Please enter captcha',
            'login_failed': 'Login failed',
            'captcha_load_fail': 'Load failed, click to retry',

            // Register page
            'register_subtitle': 'Create a new account',
            'register_name': 'Nickname',
            'register_email': 'Email',
            'register_password': 'Password (min 6 chars)',
            'register_password_confirm': 'Confirm password',
            'register_captcha': 'Captcha answer',
            'register_btn': 'Sign Up',
            'register_has_account': 'Already have an account? ',
            'register_login_link': 'Sign In',
            'register_error_email': 'Please enter email',
            'register_error_password': 'Please enter password',
            'register_error_password_length': 'Password must be at least 6 characters',
            'register_error_password_mismatch': 'Passwords do not match',
            'register_error_captcha': 'Please enter captcha',
            'register_failed': 'Registration failed',
            'register_success': 'Registration successful, please check your email for verification',

            // Verify page
            'verify_title': 'Email Verification',
            'verify_loading': 'Verifying...',
            'verify_invalid_link': 'Invalid verification link',
            'verify_failed': 'Verification failed',
            'verify_success': 'Email verified successfully',
            'verify_go_login': 'Go to Login',

            // Admin login
            'admin_login_title': 'Admin Login',
            'admin_username': 'Admin username',
            'admin_password': 'Admin password',
            'admin_login_btn': 'Sign In',
            'admin_setup_hint': 'First time setup - create admin account',
            'admin_setup_username': 'Set admin username',
            'admin_setup_password': 'Set admin password',
            'admin_setup_password_confirm': 'Confirm password',
            'admin_setup_btn': 'Create Admin',
            'admin_error_username': 'Please enter username',
            'admin_error_password': 'Please enter password',
            'admin_error_password_length': 'Password must be at least 6 characters',
            'admin_error_password_mismatch': 'Passwords do not match',
            'admin_error_credentials': 'Please enter username and password',
            'admin_error_wrong_credentials': 'Invalid username or password',
            'admin_setup_failed': 'Setup failed',
            'admin_login_failed': 'Login failed',
            'admin_login_retry': 'Login failed, please try again',

            // Chat page
            'chat_title': 'Software Self-Service Platform',
            'chat_login_btn': 'Sign In',
            'chat_logout_btn': 'Sign Out',
            'chat_welcome_title': 'Welcome to Software Self-Service Platform',
            'chat_welcome_desc': 'Enter your question and I will find relevant information for you.',
            'chat_input_placeholder': 'Type your question... (paste images supported)',
            'chat_image_preview_alt': 'Preview',
            'chat_image_remove_title': 'Remove image',
            'chat_image_recognize': 'Please identify the content of this image',
            'chat_request_failed': 'Request failed',
            'chat_no_answer': 'No answer available',
            'chat_pending_message': 'This question has been forwarded to support staff, please check back later',
            'chat_error_prefix': 'Sorry, an error occurred: ',
            'chat_error_suffix': '. Please try again later.',
            'chat_error_unknown': 'Unknown error',
            'chat_user_image_alt': 'User image',
            'chat_source_toggle': 'Sources',
            'chat_source_unknown': 'Unknown document',
            'chat_source_image': '📷 Image source',

            // Admin panel - sidebar
            'admin_panel_title': 'Admin Panel',
            'admin_nav_documents': 'Documents',
            'admin_nav_pending': 'Pending Questions',
            'admin_nav_knowledge': 'Knowledge Entry',
            'admin_nav_settings': 'Settings',
            'admin_nav_users': 'User Management',
            'admin_sidebar_logout': 'Sign Out',

            // Admin - documents
            'admin_doc_title': 'Document Management',
            'admin_doc_drop_text': 'Drag files here, or click to select',
            'admin_doc_drop_hint': 'Supports PDF, Word, Excel, PPT, Markdown',
            'admin_doc_url_placeholder': 'Enter document URL',
            'admin_doc_url_submit': 'Submit URL',
            'admin_doc_list_title': 'Document List',
            'admin_doc_th_name': 'Document Name',
            'admin_doc_th_type': 'File Type',
            'admin_doc_th_status': 'Status',
            'admin_doc_th_time': 'Upload Time',
            'admin_doc_th_action': 'Action',
            'admin_doc_empty': 'No documents',
            'admin_doc_status_processing': 'Processing',
            'admin_doc_status_success': 'Success',
            'admin_doc_status_failed': 'Failed',
            'admin_doc_delete_btn': 'Delete',
            'admin_doc_uploading': 'Uploading {name}...',
            'admin_doc_upload_success': 'File uploaded successfully',
            'admin_doc_upload_failed': 'Upload failed',
            'admin_doc_url_empty': 'Please enter a URL',
            'admin_doc_url_submitting': 'Submitting URL...',
            'admin_doc_url_success': 'URL submitted successfully',
            'admin_doc_url_failed': 'Submission failed',
            'admin_doc_load_failed': 'Load failed',

            // Admin - delete dialog
            'admin_delete_title': 'Confirm Delete',
            'admin_delete_msg': 'Are you sure you want to delete "{name}"? This cannot be undone.',
            'admin_delete_default_msg': 'Are you sure you want to delete this document? This cannot be undone.',
            'admin_delete_cancel': 'Cancel',
            'admin_delete_confirm': 'Delete',
            'admin_delete_success': 'Document deleted',
            'admin_delete_failed': 'Delete failed',

            // Admin - pending questions
            'admin_pending_title': 'Pending Questions',
            'admin_pending_filter_all': 'All',
            'admin_pending_filter_pending': 'Pending',
            'admin_pending_filter_answered': 'Answered',
            'admin_pending_empty': 'No questions',
            'admin_pending_user': 'User',
            'admin_pending_answer_prefix': 'Answer',
            'admin_pending_answer_btn': 'Answer',
            'admin_pending_edit_btn': 'Edit',
            'admin_pending_delete_btn': 'Delete',
            'admin_pending_delete_confirm': 'Are you sure you want to delete this question?',
            'admin_pending_deleted': 'Deleted',

            // Admin - answer dialog
            'admin_answer_title': 'Answer Question',
            'admin_answer_text_label': 'Text Answer',
            'admin_answer_text_placeholder': 'Enter your answer',
            'admin_answer_image_label': 'Images (optional, paste/drag/click to upload)',
            'admin_answer_image_upload': 'Click to select, drag or paste images here',
            'admin_answer_url_label': 'Related URL (optional)',
            'admin_answer_cancel': 'Cancel',
            'admin_answer_submit': 'Submit Answer',
            'admin_answer_empty': 'Please enter an answer or upload images',
            'admin_answer_success': 'Answer submitted',
            'admin_answer_failed': 'Submission failed',

            // Admin - settings
            'admin_settings_title': 'System Settings',
            'admin_settings_server_port': 'Server Port',
            'admin_settings_http_port': 'HTTP Port',
            'admin_settings_port_hint': 'Server restart required after changing port',
            'admin_settings_restart': 'Restart Server',
            'admin_settings_restart_confirm': 'Are you sure you want to restart the server? Service will be briefly unavailable.',
            'admin_settings_restarting': 'Server is restarting, please refresh shortly...',
            'admin_settings_restart_failed': 'Restart failed',
            'admin_settings_llm': 'LLM Configuration',
            'admin_settings_llm_endpoint': 'LLM Endpoint',
            'admin_settings_llm_model': 'LLM Model',
            'admin_settings_api_key': 'API Key',
            'admin_settings_temperature': 'Temperature',
            'admin_settings_max_tokens': 'Max Tokens',
            'admin_settings_embedding': 'Embedding Configuration',
            'admin_settings_emb_endpoint': 'Embedding Endpoint',
            'admin_settings_emb_model': 'Embedding Model',
            'admin_settings_emb_multimodal': 'Multimodal Embedding',
            'admin_settings_emb_multimodal_no': 'No (standard /embeddings)',
            'admin_settings_emb_multimodal_yes': 'Yes (/embeddings/multimodal)',
            'admin_settings_emb_multimodal_hint': 'Enable for Doubao vision embedding model',
            'admin_settings_get_api_key': 'Get API Key',
            'admin_settings_vector': 'Vector Configuration',
            'admin_settings_chunk_size': 'Chunk Size',
            'admin_settings_overlap': 'Overlap Size',
            'admin_settings_topk': 'Top-K',
            'admin_settings_threshold': 'Similarity Threshold',
            'admin_settings_content_priority': 'Content Priority',
            'admin_settings_priority_image': 'Prefer image+text (prioritize results with images)',
            'admin_settings_priority_text': 'Prefer text only (prioritize plain text results)',
            'admin_settings_priority_hint': 'Set whether to prioritize image+text or plain text in answers',
            'admin_settings_smtp': 'Email Server (SMTP)',
            'admin_settings_smtp_host': 'SMTP Server',
            'admin_settings_smtp_port': 'Port',
            'admin_settings_smtp_tls': 'Use TLS',
            'admin_settings_smtp_tls_yes': 'Yes',
            'admin_settings_smtp_tls_no': 'No',
            'admin_settings_smtp_username': 'Username',
            'admin_settings_smtp_password': 'Password',
            'admin_settings_smtp_from_addr': 'From Address',
            'admin_settings_smtp_from_name': 'From Name',
            'admin_settings_smtp_test': 'Test Send',
            'admin_settings_smtp_test_placeholder': 'Recipient email',
            'admin_settings_smtp_test_btn': 'Send Test Email',
            'admin_settings_smtp_test_empty': 'Please enter recipient email',
            'admin_settings_smtp_test_sending': 'Sending...',
            'admin_settings_smtp_test_success': 'Test email sent, please check inbox',
            'admin_settings_smtp_test_failed': 'Send failed',
            'admin_settings_admin': 'Admin Settings',
            'admin_settings_login_route': 'Admin Login Route',
            'admin_settings_login_route_hint': 'Access this hidden route to reach admin login page',
            'admin_settings_product_intro': 'Product Introduction',
            'admin_settings_product_intro_label': 'Welcome Message',
            'admin_settings_product_intro_placeholder': 'Enter product intro, shown as welcome message after login',
            'admin_settings_product_intro_hint': 'Displayed on chat page after user login',
            'admin_settings_product_name': 'Product Name',
            'admin_settings_product_name_label': 'Product Name',
            'admin_settings_product_name_placeholder': 'Enter product name, e.g.: XX Self-Service System',
            'admin_settings_product_name_hint': 'Displayed in page title, login page, chat page, etc. Auto-translated for different languages',
            'admin_settings_save': 'Save Settings',
            'admin_settings_no_changes': 'No changes to save',
            'admin_settings_saved': 'Settings saved',
            'admin_settings_save_failed': 'Save failed',
            'admin_settings_load_failed': 'Failed to load settings',
            'admin_settings_not_set': 'Not set',

            // Admin - knowledge
            'admin_knowledge_title': 'Knowledge Entry',
            'admin_knowledge_legend': 'Add Knowledge Entry',
            'admin_knowledge_title_label': 'Title',
            'admin_knowledge_title_placeholder': 'Knowledge entry title',
            'admin_knowledge_content_label': 'Content',
            'admin_knowledge_content_placeholder': 'Enter knowledge content (detailed description supported)',
            'admin_knowledge_image_label': 'Images (optional)',
            'admin_knowledge_image_upload': 'Click to upload, drag images here, or paste from clipboard',
            'admin_knowledge_image_hint': 'Supports JPG, PNG, GIF, WebP, BMP formats',
            'admin_knowledge_submit': 'Submit Entry',
            'admin_knowledge_empty': 'Please enter title and content',
            'admin_knowledge_submitting': 'Submitting knowledge...',
            'admin_knowledge_success': 'Knowledge entry submitted',
            'admin_knowledge_failed': 'Submission failed',

            // Admin - users
            'admin_users_title': 'User Management',
            'admin_users_add_legend': 'Add Admin Account',
            'admin_users_username': 'Username',
            'admin_users_password': 'Password (min 6 chars)',
            'admin_users_role': 'Role',
            'admin_users_role_editor': 'Editor (content management / answer questions)',
            'admin_users_role_super': 'Super Admin (full access)',
            'admin_users_role_hint': 'Editors can only manage documents, answer questions and add knowledge. Cannot modify settings or manage users.',
            'admin_users_add_btn': 'Add User',
            'admin_users_list_legend': 'Admin Account List',
            'admin_users_th_username': 'Username',
            'admin_users_th_role': 'Role',
            'admin_users_th_time': 'Created',
            'admin_users_th_action': 'Action',
            'admin_users_empty': 'No sub-accounts',
            'admin_users_role_editor_short': 'Editor',
            'admin_users_role_super_short': 'Super Admin',
            'admin_users_delete_btn': 'Delete',
            'admin_users_delete_confirm': 'Are you sure you want to delete user "{name}"?',
            'admin_users_deleted': 'User deleted',
            'admin_users_create_empty': 'Please enter username and password',
            'admin_users_create_password_short': 'Password must be at least 6 characters',
            'admin_users_created': 'User created successfully',
            'admin_users_create_failed': 'Creation failed',

            // Image upload common
            'image_select_error': 'Please select an image file',
            'image_size_error': 'Image size cannot exceed 10MB',
            'image_upload_failed': 'Image upload failed',
            'image_remove_label': 'Remove image',

            // Language
            'lang_switch': '中文'
        }
    };

    // --- Core i18n functions ---

    function getLang() {
        return localStorage.getItem(LANG_KEY) || 'zh-CN';
    }

    function setLang(lang) {
        localStorage.setItem(LANG_KEY, lang);
    }

    function t(key, params) {
        var lang = getLang();
        var dict = translations[lang] || translations['zh-CN'];
        var str = dict[key];
        if (str === undefined) {
            // Fallback to zh-CN
            str = translations['zh-CN'][key] || key;
        }
        if (params) {
            Object.keys(params).forEach(function (k) {
                str = str.replace('{' + k + '}', params[k]);
            });
        }
        return str;
    }

    function applyI18nToPage() {
        // Update page title
        document.title = t('site_title');

        // Update all elements with data-i18n attribute (textContent)
        var els = document.querySelectorAll('[data-i18n]');
        els.forEach(function (el) {
            el.textContent = t(el.getAttribute('data-i18n'));
        });

        // Update all elements with data-i18n-placeholder (placeholder)
        var placeholders = document.querySelectorAll('[data-i18n-placeholder]');
        placeholders.forEach(function (el) {
            el.placeholder = t(el.getAttribute('data-i18n-placeholder'));
        });

        // Update all elements with data-i18n-title (title attribute)
        var titles = document.querySelectorAll('[data-i18n-title]');
        titles.forEach(function (el) {
            el.title = t(el.getAttribute('data-i18n-title'));
        });

        // Update all elements with data-i18n-html (innerHTML)
        var htmlEls = document.querySelectorAll('[data-i18n-html]');
        htmlEls.forEach(function (el) {
            var key = el.getAttribute('data-i18n-html');
            el.innerHTML = t(key);
        });

        // Update html lang attribute
        var lang = getLang();
        document.documentElement.lang = lang === 'en-US' ? 'en' : 'zh-CN';

        // Update language switch button text
        var langBtns = document.querySelectorAll('.lang-switch-btn');
        langBtns.forEach(function (btn) {
            btn.textContent = t('lang_switch');
        });
    }

    function toggleLang() {
        var current = getLang();
        var next = current === 'zh-CN' ? 'en-US' : 'zh-CN';
        setLang(next);
        applyI18nToPage();
    }

    // Expose globally
    window.i18n = {
        t: t,
        getLang: getLang,
        setLang: setLang,
        applyI18nToPage: applyI18nToPage,
        toggleLang: toggleLang
    };

})();
